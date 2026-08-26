package httpapi

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"internal-image-studio/internal/promptrefiner"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/providerurl"
)

const (
	maxRefinerBodyBytes  = 128 << 10
	maxRefinerRunes      = 32_768
	promptRefinerTimeout = 60 * time.Second
	promptRefinerRate    = 10.0 / 60.0
	promptRefinerBurst   = 2.0
)

type promptRefineLimitEntry struct {
	tokens   float64
	updated  time.Time
	inFlight bool
}

type promptRefineLimiter struct {
	mu      sync.Mutex
	entries map[uuid.UUID]promptRefineLimitEntry
}

func newPromptRefineLimiter() *promptRefineLimiter {
	return &promptRefineLimiter{entries: make(map[uuid.UUID]promptRefineLimitEntry)}
}

func (l *promptRefineLimiter) acquire(userID uuid.UUID, now time.Time) (release func(), code string) {
	l.mu.Lock()
	entry, ok := l.entries[userID]
	if !ok {
		if len(l.entries) >= 10_000 {
			cutoff := now.Add(-15 * time.Minute)
			for id, candidate := range l.entries {
				if !candidate.inFlight && candidate.updated.Before(cutoff) {
					delete(l.entries, id)
				}
			}
			if len(l.entries) >= 10_000 {
				l.mu.Unlock()
				return nil, "PROMPT_REFINER_CAPACITY"
			}
		}
		entry = promptRefineLimitEntry{tokens: promptRefinerBurst, updated: now}
	} else {
		entry.tokens = min(promptRefinerBurst, entry.tokens+now.Sub(entry.updated).Seconds()*promptRefinerRate)
		entry.updated = now
	}
	if entry.inFlight {
		l.entries[userID] = entry
		l.mu.Unlock()
		return nil, "PROMPT_REFINER_BUSY"
	}
	if entry.tokens < 1 {
		l.entries[userID] = entry
		l.mu.Unlock()
		return nil, "PROMPT_REFINER_RATE_LIMITED"
	}
	entry.tokens--
	entry.inFlight = true
	l.entries[userID] = entry
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		entry := l.entries[userID]
		entry.inFlight = false
		l.entries[userID] = entry
		l.mu.Unlock()
	}, ""
}

type promptDiagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Used     int    `json:"used,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type promptRefineResponse struct {
	PolicyVersion   string                  `json:"policy_version"`
	Status          string                  `json:"status"`
	Segments        []promptrefiner.Segment `json:"segments"`
	Findings        []promptrefiner.Finding `json:"findings"`
	Diagnostics     []promptDiagnostic      `json:"diagnostics"`
	OptimizedPrompt *string                 `json:"optimized_prompt"`
	Changed         bool                    `json:"changed"`
	RefinementID    *uuid.UUID              `json:"refinement_id,omitempty"`
}

func (s *Server) refinePrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	r.Body = http.MaxBytesReader(w, r.Body, maxRefinerBodyBytes)
	var request struct {
		generationRequest
		PendingReferenceCount int `json:"pending_reference_count"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	input := request.generationRequest
	if request.PendingReferenceCount < 0 || request.PendingReferenceCount > 10 {
		writeError(w, http.StatusUnprocessableEntity, "REFERENCE_INVALID", "待上传参考图数量无效", false, r)
		return
	}
	if utf8.RuneCountInString(input.Prompt) > maxRefinerRunes {
		writeError(w, http.StatusRequestEntityTooLarge, "PROMPT_TOO_LARGE", "提示词最多可检查 32,768 个字符", false, r)
		return
	}
	model, ok := s.catalog.Find(input.ModelID)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "MODEL_UNAVAILABLE", "所选模型不可用", false, r)
		return
	}
	if input.CapabilityRevision != s.catalog.Hash {
		writeError(w, http.StatusConflict, "CAPABILITY_STALE", "模型能力已更新，请刷新后重试", false, r)
		return
	}

	result := s.promptRefiner.Refine(input.Prompt)
	diagnostics := make([]promptDiagnostic, 0, 4)
	normalized := input
	referenceCount := len(input.InputAssetIDs) + request.PendingReferenceCount
	if err := normalizeGenerationOptions(model.ID, model.Provider, model.Capabilities.MidjourneyVersions, model.Capabilities.Qualities, model.Capabilities.PromptOptimizationModes, referenceCount, &normalized); err != nil {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CAPABILITY_INVALID", Severity: "warning", Message: "当前模型参数需要调整：" + err.Error()})
	}
	allowedRatios := model.AspectRatiosForResolution(normalized.Resolution)
	ratioValid := len(allowedRatios) == 0 && normalized.AspectRatio == "auto" || slices.Contains(allowedRatios, normalized.AspectRatio)
	resolutionValid := len(model.Capabilities.Resolutions) == 0 && normalized.Resolution == "auto" || slices.Contains(model.Capabilities.Resolutions, normalized.Resolution) || model.Provider == "legnext" && len(model.Capabilities.MidjourneyVersions) > 0
	if !ratioValid || !resolutionValid || normalized.DrawCount < model.Capabilities.DrawCount.Min || normalized.DrawCount > model.Capabilities.DrawCount.Max {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CAPABILITY_INVALID", Severity: "warning", Message: "画幅、分辨率或抽卡次数不在当前模型支持范围内"})
	}
	if referenceCount > model.Capabilities.MaxReferenceImages || referenceCount > 0 && !model.Capabilities.ImageToImage || hasDuplicateAssetIDs(normalized.InputAssetIDs) {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "REFERENCE_INVALID", Severity: "warning", Message: "参考图数量或能力不受当前模型支持"})
	}
	if model.Provider == "legnext" && containsControlledLegnextInput(input.Prompt) {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "CONTROLLED_PROVIDER_INPUT", Severity: "warning", Message: "请移除原始 Midjourney 参数、外部图片链接或花括号结构；这些内容由 Cornfield 统一生成"})
	}

	canonical := provider.CanonicalRequest{
		Model: model.ProviderModel, Prompt: strings.TrimSpace(input.Prompt), AspectRatio: normalized.AspectRatio,
		PromptAspectRatio: model.PromptAspectRatio, Resolution: normalized.Resolution, ExpectedImages: model.OutputsPerDraw,
		RequestParameters: append([]string(nil), model.RequestParameters...), Options: normalized.Options,
	}
	if model.PromptSuffix != "" {
		canonical.Prompt += " " + model.PromptSuffix
	}
	if model.Provider == "legnext" && referenceCount > 0 {
		references := make([]string, 0, referenceCount)
		var err error
		if len(normalized.InputAssetIDs) > 0 {
			references, err = s.refinerReferenceURLs(r, normalized.InputAssetIDs)
		}
		if err != nil {
			diagnostics = append(diagnostics, promptDiagnostic{Code: "REFERENCE_UNAVAILABLE", Severity: "warning", Message: "部分参考图已不可用，请重新选择"})
		} else {
			if request.PendingReferenceCount > 0 {
				placeholder, signErr := providerurl.Sign(s.cfg.PublicURL, s.cfg.ProviderURLSigningSecret, uuid.Nil, ".jpeg", time.Now().Add(time.Hour))
				if signErr != nil {
					diagnostics = append(diagnostics, promptDiagnostic{Code: "REFERENCE_UNAVAILABLE", Severity: "warning", Message: "参考图长度暂时无法校验，请稍后重试"})
				} else {
					for range request.PendingReferenceCount {
						references = append(references, placeholder)
					}
				}
			}
			canonical.ReferenceURLs = references
		}
	}
	finalPrompt := canonical.Prompt
	switch model.Provider {
	case "legnext":
		if value, err := provider.BuildLegnextPrompt(canonical); err == nil {
			finalPrompt = value
		}
	case "openrouter":
		finalPrompt = provider.BuildOpenRouterPrompt(canonical)
	}
	finalLength := utf8.RuneCountInString(finalPrompt)
	if finalLength > 8192 {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "PROMPT_TOO_LONG", Severity: "warning", Message: "拼接模型参数后的提示词超过 Cornfield 生成上限", Used: finalLength, Limit: 8192})
	}
	if model.Provider == "legnext" && finalLength > 1024 {
		diagnostics = append(diagnostics, promptDiagnostic{Code: "MIDJOURNEY_COMPATIBILITY_LIMIT", Severity: "warning", Message: "最终提示词超过 Midjourney 兼容长度，建议精简后再生成", Used: finalLength, Limit: 1024})
	}
	status := result.Status
	if len(diagnostics) > 0 {
		status = "findings"
	}
	riskValues := make([]string, 0, len(result.Findings))
	for _, finding := range result.Findings {
		riskValues = append(riskValues, finding.Category)
	}
	refinementID := uuid.New()
	metricStarted := time.Now()
	metric := promptRefinementMetric{
		ID: refinementID, ModelID: model.ID, PolicyVersion: result.PolicyVersion,
		InputRunes: utf8.RuneCountInString(input.Prompt), RiskCategories: promptRiskCategories(riskValues),
	}
	recordMetric := func(outcome string, output *string, applied bool, usage provider.PromptOptimizationResult) error {
		metric.Outcome = outcome
		metric.LatencyMS = int(max(0, time.Since(metricStarted).Milliseconds()))
		metric.Changed = applied
		metric.PromptTokens = promptMetricTokenCount(usage.PromptTokens)
		metric.CompletionTokens = promptMetricTokenCount(usage.CompletionTokens)
		if output != nil {
			metric.OutputRunes = promptMetricOutputRunes(*output)
			metric.EditDistanceBucket = promptEditDistanceBucket(strings.TrimSpace(input.Prompt), *output)
		}
		return s.createPromptRefinementMetric(metric)
	}
	recordFailureMetric := func(outcome string, output *string, usage provider.PromptOptimizationResult) {
		if err := recordMetric(outcome, output, false, usage); err != nil && s.log != nil {
			s.log.Warn("prompt refinement metric write failed", "outcome", outcome, "error", err)
		}
	}
	var optimizedPrompt *string
	var optimizationUsage provider.PromptOptimizationResult
	changed := false
	if s.promptOptimizer != nil {
		limit := 8192
		if model.Provider == "legnext" {
			limit = 1024
		}
		overhead := max(0, finalLength-utf8.RuneCountInString(strings.TrimSpace(input.Prompt)))
		outputLimit := limit - overhead
		if outputLimit < 1 {
			recordFailureMetric("validation_error", nil, provider.PromptOptimizationResult{})
			writeError(w, http.StatusUnprocessableEntity, "PROMPT_REFINER_INVALID_INPUT", "当前模型参数占用过长，请先精简参数或参考图", false, r)
			return
		}
		if s.promptRefineLimit == nil || s.promptRefinerSem == nil {
			writeError(w, http.StatusServiceUnavailable, "PROMPT_REFINER_UNAVAILABLE", "提示词优化服务暂不可用，原提示词未被修改", true, r)
			return
		}
		releaseUser, limitCode := s.promptRefineLimit.acquire(currentSession(r).UserID, time.Now())
		if releaseUser == nil {
			message := "提示词优化正在处理中，请稍候"
			if limitCode == "PROMPT_REFINER_RATE_LIMITED" {
				message = "提示词优化操作过于频繁，请稍后再试"
			}
			writeError(w, http.StatusTooManyRequests, limitCode, message, true, r)
			return
		}
		defer releaseUser()
		select {
		case s.promptRefinerSem <- struct{}{}:
			defer func() { <-s.promptRefinerSem }()
		default:
			writeError(w, http.StatusTooManyRequests, "PROMPT_REFINER_CAPACITY", "提示词优化任务较多，请稍后再试", true, r)
			return
		}
		optimizeCtx, cancel := context.WithTimeout(r.Context(), promptRefinerTimeout)
		currentFindings := make([]provider.PromptOptimizationFinding, 0, min(32, len(result.Findings)))
		for _, finding := range result.Findings[:min(32, len(result.Findings))] {
			currentFindings = append(currentFindings, provider.PromptOptimizationFinding{
				Category: finding.Category, Mode: finding.Mode,
				Original: boundedRefinerContext(finding.Original, 128), Reason: boundedRefinerContext(finding.Reason, 256),
			})
		}
		constraints := make([]string, 0, min(8, len(diagnostics)))
		for _, diagnostic := range diagnostics[:min(8, len(diagnostics))] {
			constraints = append(constraints, diagnostic.Code+": "+boundedRefinerContext(diagnostic.Message, 256))
		}
		optimization, optimizeErr := s.promptOptimizer.Optimize(optimizeCtx, provider.PromptOptimizationRequest{
			Prompt: input.Prompt, TargetProvider: model.Provider, TargetModel: model.ProviderModel, MaxRunes: outputLimit,
			DeterministicFindings: currentFindings, Constraints: constraints,
		})
		cancel()
		if optimizeErr != nil {
			recordFailureMetric("provider_error", nil, provider.PromptOptimizationResult{})
			statusCode, code, message, retryable := promptOptimizerHTTPError(optimizeErr)
			writeError(w, statusCode, code, message, retryable, r)
			return
		}
		candidate := strings.TrimSpace(optimization.Prompt)
		if err := validateOptimizedPrompt(input.Prompt, candidate, model.Provider, model.PromptSuffix, canonical, limit, finalLength > limit, result, s.promptRefiner); err != nil {
			recordFailureMetric("validation_error", &candidate, optimization)
			writeError(w, http.StatusBadGateway, "PROMPT_REFINER_INVALID_RESPONSE", "优化结果未通过安全校验，原提示词未被修改", true, r)
			return
		}
		optimizedPrompt = &candidate
		optimizationUsage = optimization
		changed = candidate != strings.TrimSpace(input.Prompt)
	}
	metricOutput := optimizedPrompt
	if metricOutput == nil {
		value := strings.TrimSpace(input.Prompt)
		metricOutput = &value
	}
	outcome := "unchanged"
	if changed {
		outcome = "optimized"
	}
	if err := recordMetric(outcome, metricOutput, changed, optimizationUsage); err != nil && s.log != nil {
		s.log.Warn("prompt refinement metric write failed", "outcome", outcome, "error", err)
	}
	writeJSON(w, http.StatusOK, promptRefineResponse{
		PolicyVersion: result.PolicyVersion, Status: status, Segments: result.Segments,
		Findings: result.Findings, Diagnostics: diagnostics, OptimizedPrompt: optimizedPrompt, Changed: changed, RefinementID: &refinementID,
	})
}

func promptOptimizerHTTPError(err error) (status int, code, message string, retryable bool) {
	var typed *provider.Error
	if errors.As(err, &typed) {
		switch typed.Code {
		case "PROMPT_REFINER_TIMEOUT":
			return http.StatusGatewayTimeout, typed.Code, "提示词优化超时，原提示词未被修改", true
		case "PROMPT_REFINER_KEYS_BUSY", "PROMPT_REFINER_HTTP_429":
			return http.StatusTooManyRequests, typed.Code, "提示词优化服务繁忙，请稍后再试", true
		case "PROMPT_REFINER_INVALID_RESPONSE":
			return http.StatusBadGateway, typed.Code, "优化结果格式异常，原提示词未被修改", true
		}
	}
	return http.StatusServiceUnavailable, "PROMPT_REFINER_UNAVAILABLE", "提示词优化服务暂不可用，原提示词未被修改", true
}

func boundedRefinerContext(value string, maximum int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func validateOptimizedPrompt(original, candidate, providerID, promptSuffix string, canonical provider.CanonicalRequest, limit int, sourceOverLimit bool, before promptrefiner.Result, engine *promptrefiner.Engine) error {
	if candidate == "" || utf8.RuneCountInString(candidate) > limit || !conservativePromptRewrite(original, candidate, sourceOverLimit, len(before.Findings) > 0) || !preservesProtectedPromptTokens(original, candidate) || !preservesLatinPromptAnchors(original, candidate, before.Findings) {
		return errors.New("invalid prompt rewrite")
	}
	if providerID == "legnext" && containsControlledLegnextInput(candidate) {
		return errors.New("controlled provider input remains")
	}
	canonical.Prompt = candidate
	if promptSuffix != "" {
		canonical.Prompt += " " + promptSuffix
	}
	finalPrompt := candidate
	if providerID == "legnext" {
		value, err := provider.BuildLegnextPrompt(canonical)
		if err != nil {
			return err
		}
		finalPrompt = value
	} else if providerID == "openrouter" {
		finalPrompt = provider.BuildOpenRouterPrompt(canonical)
	}
	if utf8.RuneCountInString(finalPrompt) > limit {
		return errors.New("final prompt exceeds target limit")
	}
	knownRules := make(map[string]struct{}, len(before.Findings))
	actionableRules := make(map[string]struct{}, len(before.Findings))
	for _, finding := range before.Findings {
		knownRules[finding.RuleID] = struct{}{}
		if finding.Mode == "mapped" || finding.Mode == "manual_only" {
			actionableRules[finding.RuleID] = struct{}{}
		}
	}
	for _, finding := range engine.Refine(candidate).Findings {
		if finding.Mode == "manual_only" {
			return errors.New("manual-only finding remains")
		}
		if _, remains := actionableRules[finding.RuleID]; remains {
			return errors.New("actionable finding remains")
		}
		if _, existed := knownRules[finding.RuleID]; !existed {
			return errors.New("rewrite introduced a new deterministic finding")
		}
	}
	return nil
}

func conservativePromptRewrite(original, candidate string, sourceOverLimit, hasKnownFindings bool) bool {
	originalRunes := []rune(strings.TrimSpace(original))
	candidateRunes := []rune(strings.TrimSpace(candidate))
	if !sourceOverLimit && len(originalRunes) >= 8 {
		if len(candidateRunes) < len(originalRunes)/2 || len(candidateRunes) > len(originalRunes)+max(64, len(originalRunes)/4) {
			return false
		}
		minimumSimilarity := 0.20
		if len(originalRunes) >= 40 {
			minimumSimilarity = 0.30
		}
		if hasKnownFindings {
			// A short prompt made mostly of unsafe wording may legitimately need
			// a larger edit. Still reject a wholly unrelated completion.
			minimumSimilarity = 0.02
		}
		if len(originalRunes) >= 80 {
			minimumSimilarity = 0.45
		}
		if len(originalRunes) <= 8192 && promptBigramSimilarity(originalRunes, candidateRunes) < minimumSimilarity {
			return false
		}
	}
	originalHan, originalLatin := promptScriptCounts(originalRunes)
	candidateHan, candidateLatin := promptScriptCounts(candidateRunes)
	if originalHan > originalLatin*3 && candidateLatin > candidateHan || originalLatin > originalHan*3 && candidateHan > candidateLatin {
		return false
	}
	return true
}

var protectedPromptPattern = regexp.MustCompile(`(?i)\d+(?:[.,]\d+)*(?:\s*(?::|x|×)\s*\d+(?:[.,]\d+)*)?|"(?:[^"\\]|\\.)*"|'[^'\n]*'|“[^”\n]*”|‘[^’\n]*’|「[^」\n]*」|『[^』\n]*』`)
var latinPromptAnchorPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9_-]*`)

func preservesProtectedPromptTokens(original, candidate string) bool {
	want := make(map[string]int)
	for _, token := range protectedPromptPattern.FindAllString(original, -1) {
		want[token]++
	}
	got := make(map[string]int)
	for _, token := range protectedPromptPattern.FindAllString(candidate, -1) {
		got[token]++
	}
	if len(want) != len(got) {
		return false
	}
	for token, count := range want {
		if got[token] != count {
			return false
		}
	}
	return true
}

func preservesLatinPromptAnchors(original, candidate string, findings []promptrefiner.Finding) bool {
	excluded := make(map[string]struct{})
	for _, finding := range findings {
		for _, token := range latinPromptAnchorPattern.FindAllString(strings.ToLower(finding.Original), -1) {
			excluded[token] = struct{}{}
		}
	}
	candidateTokens := make(map[string]struct{})
	for _, token := range latinPromptAnchorPattern.FindAllString(strings.ToLower(candidate), -1) {
		candidateTokens[token] = struct{}{}
	}
	for _, token := range latinPromptAnchorPattern.FindAllString(strings.ToLower(original), -1) {
		if len(token) < 3 {
			continue
		}
		if _, isFinding := excluded[token]; isFinding {
			continue
		}
		if _, preserved := candidateTokens[token]; !preserved {
			return false
		}
	}
	return true
}

func promptBigramSimilarity(left, right []rune) float64 {
	normalize := func(value []rune) []rune {
		result := make([]rune, 0, len(value))
		for _, character := range value {
			if !unicode.IsSpace(character) {
				result = append(result, unicode.ToLower(character))
			}
		}
		return result
	}
	left, right = normalize(left), normalize(right)
	if len(left) < 2 || len(right) < 2 {
		if string(left) == string(right) {
			return 1
		}
		return 0
	}
	counts := make(map[[2]rune]int, len(left)-1)
	for index := 1; index < len(left); index++ {
		counts[[2]rune{left[index-1], left[index]}]++
	}
	intersection := 0
	for index := 1; index < len(right); index++ {
		pair := [2]rune{right[index-1], right[index]}
		if counts[pair] > 0 {
			counts[pair]--
			intersection++
		}
	}
	return 2 * float64(intersection) / float64(len(left)+len(right)-2)
}

func promptScriptCounts(value []rune) (han, latin int) {
	for _, character := range value {
		switch {
		case unicode.Is(unicode.Han, character):
			han++
		case unicode.Is(unicode.Latin, character):
			latin++
		}
	}
	return han, latin
}

func (s *Server) refinerReferenceURLs(r *http.Request, ids []uuid.UUID) ([]string, error) {
	sess := currentSession(r)
	urls := make([]string, 0, len(ids))
	for _, id := range ids {
		var storageKey string
		err := s.db.QueryRow(r.Context(), `SELECT storage_key FROM assets
			WHERE id=$1 AND owner_user_id=$2 AND purge_pending=false AND purged_at IS NULL AND expires_at>now()`, id, sess.UserID).Scan(&storageKey)
		if err != nil {
			return nil, err
		}
		value, err := providerurl.Sign(s.cfg.PublicURL, s.cfg.ProviderURLSigningSecret, id, filepath.Ext(storageKey), time.Now().Add(time.Hour))
		if err != nil {
			return nil, err
		}
		urls = append(urls, value)
	}
	return urls, nil
}
