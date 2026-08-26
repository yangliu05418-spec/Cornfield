package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/refinercanary"
)

const (
	refinerProtocolInterval = time.Second
	refinerE2EInterval      = 7 * time.Second
)

type refinerCanaryResult struct {
	CaseID         string `json:"case_id"`
	Class          string `json:"class"`
	TargetProvider string `json:"target_provider"`
	TargetModel    string `json:"target_model"`
	Status         string `json:"status"`
	ValidationCode string `json:"validation_code,omitempty"`
	DurationMS     int64  `json:"duration_ms"`
	SourceRunes    int    `json:"source_runes"`
	ResultRunes    int    `json:"result_runes,omitempty"`
	Changed        bool   `json:"changed"`
	InputTokens    int64  `json:"input_tokens,omitempty"`
	ResultTokens   int64  `json:"result_tokens,omitempty"`
	TotalTokens    int64  `json:"total_tokens,omitempty"`
}

type refinerCanaryReport struct {
	Mode               string                `json:"mode"`
	ReleaseSHA         string                `json:"release_sha"`
	CapabilityRevision string                `json:"capability_revision,omitempty"`
	RefinerModel       string                `json:"refiner_model"`
	StartedAt          time.Time             `json:"started_at"`
	CompletedAt        time.Time             `json:"completed_at"`
	Results            []refinerCanaryResult `json:"results"`
}

type refinerE2EResponse struct {
	PolicyVersion   string  `json:"policy_version"`
	OptimizedPrompt *string `json:"optimized_prompt"`
	Changed         bool    `json:"changed"`
}

func defaultCanaryReportPath(profile, release string) string {
	prefix := "canary-"
	switch profile {
	case "refiner-e2e":
		prefix = "refiner-e2e-"
	case "layer-e2e":
		prefix = "layer-e2e-"
	}
	return prefix + shortSHA(release) + ".json"
}

func runRefinerProtocol(keyFile, reportPath, release string) error {
	keys, err := readSecretLines(keyFile)
	if err != nil {
		return fmt.Errorf("read OpenRouter key pool: %w", err)
	}
	fixtures, err := refinercanary.Fixtures()
	if err != nil {
		return err
	}
	cases := refinercanary.ProtocolFixtures(fixtures)
	if len(cases) != 5 {
		return errors.New("refiner protocol corpus must contain exactly five cases")
	}
	report := refinerCanaryReport{
		Mode: "refiner-protocol", ReleaseSHA: release,
		RefinerModel: provider.OpenRouterPromptRefinerModel, StartedAt: time.Now().UTC(),
	}
	optimizer := provider.NewOpenRouterPromptOptimizer(keys, "https://corn.kumadrama.com", 35*time.Second)
	keys = nil
	permitCtx, stopPermits := context.WithCancel(context.Background())
	defer stopPermits()
	permits := newCreatePermitStream(permitCtx, refinerProtocolInterval)
	for _, fixture := range cases {
		<-permits
		started := time.Now()
		item := refinerCanaryResult{
			CaseID: fixture.ID, Class: fixture.Class, TargetProvider: fixture.TargetProvider,
			TargetModel: fixture.TargetModel, Status: "failed", SourceRunes: utf8.RuneCountInString(fixture.Original),
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		optimized, optimizeErr := optimizer.Optimize(ctx, provider.PromptOptimizationRequest{
			Prompt: fixture.Original, TargetProvider: fixture.TargetProvider,
			TargetModel: fixture.TargetModel, MaxRunes: fixture.MaxRunes,
		})
		cancel()
		item.DurationMS = time.Since(started).Milliseconds()
		if optimizeErr != nil {
			item.ValidationCode = safeRefinerErrorCode(optimizeErr)
			report.Results = append(report.Results, item)
			report.CompletedAt = time.Now().UTC()
			_ = writePrivateJSON(reportPath, report)
			return fmt.Errorf("refiner protocol case %s failed with %s", fixture.ID, item.ValidationCode)
		}
		item.ResultRunes = utf8.RuneCountInString(optimized.Prompt)
		item.Changed = strings.TrimSpace(optimized.Prompt) != strings.TrimSpace(fixture.Original)
		item.InputTokens, item.ResultTokens, item.TotalTokens = optimized.PromptTokens, optimized.CompletionTokens, optimized.TotalTokens
		if invariantErr := refinercanary.ValidateInvariant(fixture, optimized.Prompt); invariantErr != nil {
			item.ValidationCode = safeInvariantCode(invariantErr)
			report.Results = append(report.Results, item)
			report.CompletedAt = time.Now().UTC()
			_ = writePrivateJSON(reportPath, report)
			return fmt.Errorf("refiner protocol case %s failed invariant %s", fixture.ID, item.ValidationCode)
		}
		item.Status = "passed"
		report.Results = append(report.Results, item)
		if err = writePrivateJSON(reportPath, report); err != nil {
			return err
		}
		fmt.Printf("refiner protocol %s passed (%d ms)\n", fixture.ID, item.DurationMS)
	}
	report.CompletedAt = time.Now().UTC()
	return writePrivateJSON(reportPath, report)
}

func runRefinerE2E(ctx context.Context, client *apiClient, reportPath, release, revision string, catalog *modelconfig.Catalog) error {
	fixtures, err := refinercanary.Fixtures()
	if err != nil {
		return err
	}
	cases := refinercanary.E2EFixtures(fixtures)
	if len(cases) != 10 {
		return errors.New("refiner e2e corpus must contain exactly ten cases")
	}
	report := refinerCanaryReport{
		Mode: "refiner-e2e", ReleaseSHA: release, CapabilityRevision: revision,
		RefinerModel: provider.OpenRouterPromptRefinerModel, StartedAt: time.Now().UTC(),
	}
	permits := newCreatePermitStream(ctx, refinerE2EInterval)
	allPassed := true
	for _, fixture := range cases {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-permits:
		}
		started := time.Now()
		item := refinerCanaryResult{
			CaseID: fixture.ID, Class: fixture.Class, TargetProvider: fixture.TargetProvider,
			TargetModel: fixture.TargetModel, Status: "failed", SourceRunes: utf8.RuneCountInString(fixture.Original),
		}
		model, ok := refinerTargetModel(catalog, fixture)
		if !ok {
			item.ValidationCode = "REFINER_MODEL_UNAVAILABLE"
			item.DurationMS = time.Since(started).Milliseconds()
			report.Results = append(report.Results, item)
			allPassed = false
			continue
		}
		payload := refinerE2EPayload(model, revision, fixture.Original)
		var response refinerE2EResponse
		requestCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		callErr := client.json(requestCtx, http.MethodPost, "/api/v1/prompts/refine", payload, &response, "")
		cancel()
		item.DurationMS = time.Since(started).Milliseconds()
		if callErr != nil {
			item.ValidationCode = safeRefinerErrorCode(callErr)
			allPassed = false
		} else if response.OptimizedPrompt == nil || response.PolicyVersion == "" {
			item.ValidationCode = "REFINER_RESPONSE_MISSING"
			allPassed = false
		} else {
			candidate := strings.TrimSpace(*response.OptimizedPrompt)
			item.ResultRunes = utf8.RuneCountInString(candidate)
			item.Changed = candidate != strings.TrimSpace(fixture.Original)
			if response.Changed != item.Changed {
				item.ValidationCode = "REFINER_CHANGED_MISMATCH"
				allPassed = false
			} else if invariantErr := refinercanary.ValidateInvariant(fixture, candidate); invariantErr != nil {
				item.ValidationCode = safeInvariantCode(invariantErr)
				allPassed = false
			} else {
				item.Status = "passed"
			}
		}
		report.Results = append(report.Results, item)
		if err = writePrivateJSON(reportPath, report); err != nil {
			return err
		}
		fmt.Printf("refiner e2e %s %s (%d ms)\n", fixture.ID, item.Status, item.DurationMS)
	}
	report.CompletedAt = time.Now().UTC()
	if err = writePrivateJSON(reportPath, report); err != nil {
		return err
	}
	if !allPassed {
		return errors.New("one or more refiner e2e cases failed; inspect validation codes in the private report")
	}
	return nil
}

func refinerTargetModel(catalog *modelconfig.Catalog, fixture refinercanary.Fixture) (modelconfig.Model, bool) {
	for _, model := range catalog.Models {
		if model.Enabled && model.Provider == fixture.TargetProvider && model.ProviderModel == fixture.TargetModel {
			return model, true
		}
	}
	return modelconfig.Model{}, false
}

func refinerE2EPayload(model modelconfig.Model, revision, prompt string) map[string]any {
	resolution := "auto"
	if len(model.Capabilities.Resolutions) > 0 {
		resolution = model.Capabilities.Resolutions[0]
	}
	aspectRatio := "auto"
	if ratios := model.AspectRatiosForResolution(resolution); len(ratios) > 0 {
		aspectRatio = ratios[0]
	}
	drawCount := model.Capabilities.DrawCount.Default
	if drawCount < 1 {
		drawCount = max(1, model.Capabilities.DrawCount.Min)
	}
	options := provider.GenerationOptions{}
	if len(model.Capabilities.MidjourneyVersions) > 0 {
		options.Midjourney = &provider.MidjourneyOptions{
			Version: model.Capabilities.MidjourneyVersions[0], Resolution: strings.ToLower(resolution),
			Speed: "fast", Stylize: 100,
		}
	}
	if len(model.Capabilities.Qualities) > 0 || len(model.Capabilities.PromptOptimizationModes) > 0 {
		options.Image = &provider.ImageOptions{}
		if len(model.Capabilities.Qualities) > 0 {
			options.Image.Quality = model.Capabilities.Qualities[0]
		}
		if len(model.Capabilities.PromptOptimizationModes) > 0 {
			options.Image.PromptOptimizationMode = model.Capabilities.PromptOptimizationModes[0]
		}
	}
	return map[string]any{
		"model_id": model.ID, "capability_revision": revision, "prompt": prompt,
		"aspect_ratio": aspectRatio, "resolution": resolution, "draw_count": drawCount,
		"input_asset_ids": []string{}, "pending_reference_count": 0, "options": options,
	}
}

func readSecretLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		for index := range data {
			data[index] = 0
		}
	}()
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, " \t\r\n\x00") {
			return nil, errors.New("key pool contains an invalid entry")
		}
		if _, duplicate := seen[line]; duplicate {
			return nil, errors.New("key pool contains a duplicate entry")
		}
		seen[line] = struct{}{}
		result = append(result, line)
	}
	if len(result) == 0 {
		return nil, errors.New("key pool is empty")
	}
	return result, nil
}

func safeRefinerErrorCode(err error) string {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Code {
		case "PROMPT_REFINER_INVALID_INPUT", "PROMPT_REFINER_INVALID_RESPONSE", "PROMPT_REFINER_UNAVAILABLE",
			"PROMPT_REFINER_KEYS_BUSY", "PROMPT_REFINER_REQUEST_FAILED", "PROMPT_REFINER_TIMEOUT",
			"PROMPT_REFINER_HTTP_400", "PROMPT_REFINER_HTTP_401", "PROMPT_REFINER_HTTP_402",
			"PROMPT_REFINER_HTTP_403", "PROMPT_REFINER_HTTP_429", "PROMPT_REFINER_HTTP_500",
			"PROMPT_REFINER_HTTP_502", "PROMPT_REFINER_HTTP_503", "PROMPT_REFINER_HTTP_504":
			return providerErr.Code
		}
	}
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "PROMPT_REFINER_INVALID_INPUT", "PROMPT_REFINER_INVALID_RESPONSE", "PROMPT_REFINER_UNAVAILABLE",
			"PROMPT_REFINER_KEYS_BUSY", "PROMPT_REFINER_TIMEOUT", "PROMPT_REFINER_RATE_LIMITED",
			"PROMPT_REFINER_BUSY", "PROMPT_REFINER_CAPACITY", "CAPABILITY_STALE", "MODEL_UNAVAILABLE":
			return apiErr.Code
		}
	}
	return "REFINER_CANARY_ERROR"
}

func safeInvariantCode(err error) string {
	switch err.Error() {
	case "candidate_boundary", "candidate_control", "candidate_provider_syntax", "candidate_lost_anchor",
		"candidate_retained_forbidden_text", "candidate_length_drift", "candidate_language_drift":
		return strings.ToUpper(err.Error())
	default:
		return "REFINER_INVARIANT_FAILED"
	}
}
