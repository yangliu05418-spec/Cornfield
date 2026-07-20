package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
)

const (
	defaultUsername      = "Intern2"
	maximumResponseBytes = 4 << 20
)

type apiClient struct {
	base *url.URL
	http *http.Client
	csrf string
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

type modelEnvelope struct {
	Revision string `json:"revision"`
}

type folder struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type uploadState struct {
	ID         uuid.UUID  `json:"id"`
	Status     string     `json:"status"`
	AssetID    *uuid.UUID `json:"asset_id"`
	ContentURL string     `json:"content_url"`
	ErrorCode  *string    `json:"error_code"`
}

type generationOutput struct {
	AssetID uuid.UUID `json:"asset_id"`
	Width   int       `json:"width"`
	Height  int       `json:"height"`
}

type generationJob struct {
	Status       string             `json:"status"`
	ErrorCode    *string            `json:"error_code"`
	ErrorMessage *string            `json:"error_message"`
	Outputs      []generationOutput `json:"outputs"`
}

type generationBatch struct {
	ID               uuid.UUID       `json:"id"`
	Status           string          `json:"status"`
	ExpectedOutputs  int             `json:"expected_outputs"`
	CompletedOutputs int             `json:"completed_outputs"`
	Jobs             []generationJob `json:"jobs"`
}

type canaryCase struct {
	Key          string
	Model        modelconfig.Model
	Revision     string
	Mode         string
	AspectRatio  string
	Resolution   string
	Quality      string
	ReferenceID  *uuid.UUID
	ExpectedSize string
	Prompt       string
	PromptSHA256 string
}

type caseResult struct {
	Key              string             `json:"key"`
	ModelID          string             `json:"model_id"`
	Mode             string             `json:"mode"`
	AspectRatio      string             `json:"aspect_ratio"`
	Resolution       string             `json:"resolution"`
	Quality          string             `json:"quality,omitempty"`
	PromptSHA256     string             `json:"prompt_sha256"`
	BatchID          *uuid.UUID         `json:"batch_id,omitempty"`
	Status           string             `json:"status"`
	ErrorCode        string             `json:"error_code,omitempty"`
	ErrorMessage     string             `json:"error_message,omitempty"`
	ExpectedOutputs  int                `json:"expected_outputs"`
	CompletedOutputs int                `json:"completed_outputs"`
	Outputs          []generationOutput `json:"outputs,omitempty"`
	DurationMS       int64              `json:"duration_ms"`
	StartedAt        time.Time          `json:"started_at"`
	CompletedAt      time.Time          `json:"completed_at"`
}

type report struct {
	ReleaseSHA         string       `json:"release_sha"`
	CapabilityRevision string       `json:"capability_revision"`
	Username           string       `json:"username"`
	StartedAt          time.Time    `json:"started_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	CompletedAt        *time.Time   `json:"completed_at,omitempty"`
	Results            []caseResult `json:"results"`
}

type reportStore struct {
	mu     sync.Mutex
	path   string
	report report
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var baseURL, username, passwordFile, releaseSHA, configPath, reportPath string
	var allowHTTP bool
	flag.StringVar(&baseURL, "base-url", "https://corn.kumadrama.com", "Cornfield HTTPS origin")
	flag.StringVar(&username, "username", defaultUsername, "existing canary username")
	flag.StringVar(&passwordFile, "password-file", "", "root-managed file containing the canary password")
	flag.StringVar(&releaseSHA, "release", "", "deployed release commit SHA")
	flag.StringVar(&configPath, "model-config", "./config/models.yaml", "deployed model catalog")
	flag.StringVar(&reportPath, "report", "", "resumable JSON report path")
	flag.BoolVar(&allowHTTP, "allow-http", false, "allow HTTP for isolated tests only")
	flag.Parse()

	if passwordFile == "" || releaseSHA == "" {
		return errors.New("--password-file and --release are required")
	}
	if reportPath == "" {
		reportPath = "canary-" + shortSHA(releaseSHA) + ".json"
	}
	catalog, err := modelconfig.Load(configPath)
	if err != nil {
		return fmt.Errorf("load model catalog: %w", err)
	}
	password, err := readPassword(passwordFile)
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}
	client, err := newAPIClient(baseURL, allowHTTP)
	if err != nil {
		return fmt.Errorf("configure API client: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err = client.login(ctx, username, password); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	password = ""
	defer client.logout(context.Background())

	var models modelEnvelope
	if err = client.json(ctx, http.MethodGet, "/api/v1/models", nil, &models, ""); err != nil {
		return fmt.Errorf("load deployed models: %w", err)
	}
	if models.Revision != catalog.Hash {
		return fmt.Errorf("capability revision mismatch: API=%s config=%s", models.Revision, catalog.Hash)
	}
	if err = client.probeSSE(ctx); err != nil {
		return fmt.Errorf("SSE probe: %w", err)
	}

	folderID, err := client.ensureFolder(ctx, "Canary "+shortSHA(releaseSHA))
	if err != nil {
		return fmt.Errorf("create canary folder: %w", err)
	}
	referenceID, err := client.ensureReference(ctx, folderID)
	if err != nil {
		return fmt.Errorf("prepare reference image: %w", err)
	}
	store, err := openReport(reportPath, releaseSHA, catalog.Hash, username)
	if err != nil {
		return fmt.Errorf("open report: %w", err)
	}

	seed := deterministicSeed(releaseSHA)
	allPassed := true
	for _, model := range catalog.Models {
		if !model.Enabled {
			continue
		}
		cases := buildTextCases(model, catalog.Hash, releaseSHA, seed)
		if model.Capabilities.ImageToImage {
			cases = append(cases, buildImageCase(model, catalog.Hash, releaseSHA, seed, referenceID))
		}
		if err := runModel(ctx, client, store, folderID, cases); err != nil {
			allPassed = false
			fmt.Fprintf(os.Stderr, "model %s paused: %v\n", model.ID, err)
		}
	}
	if allPassed && store.allPassed() {
		now := time.Now().UTC()
		store.mu.Lock()
		store.report.CompletedAt = &now
		store.report.UpdatedAt = now
		err = store.writeLocked()
		store.mu.Unlock()
		if err != nil {
			return fmt.Errorf("finalize report: %w", err)
		}
		fmt.Printf("canary complete: %s\n", reportPath)
		return nil
	}
	return errors.New("canary incomplete or failed; resume with the same --report")
}

func newAPIClient(raw string, allowHTTP bool) (*apiClient, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return nil, errors.New("base URL must be a bare origin")
	}
	if parsed.Scheme != "https" && !(allowHTTP && parsed.Scheme == "http") {
		return nil, errors.New("base URL must use HTTPS")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &apiClient{base: parsed, http: &http.Client{Timeout: 40 * time.Second, Jar: jar}}, nil
}

func readPassword(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Size() < 1 || info.Size() > 4096 {
		return "", errors.New("password file has an invalid size")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("password file must not be readable by group or other users")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	password := strings.TrimSpace(string(data))
	if password == "" {
		return "", errors.New("password file is empty")
	}
	return password, nil
}

func (c *apiClient) login(ctx context.Context, username, password string) error {
	var response struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
		CSRF string `json:"csrf_token"`
	}
	if err := c.json(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": username, "password": password}, &response, ""); err != nil {
		return err
	}
	if !strings.EqualFold(response.User.Username, username) || response.CSRF == "" {
		return errors.New("login returned an unexpected user or missing CSRF token")
	}
	c.csrf = response.CSRF
	return nil
}

func (c *apiClient) logout(ctx context.Context) {
	_ = c.json(ctx, http.MethodPost, "/api/v1/auth/logout", nil, nil, "")
}

func (c *apiClient) json(ctx context.Context, method, path string, input, output any, idempotencyKey string) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base.ResolveReference(&url.URL{Path: path}).String(), body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, maximumResponseBytes)).Decode(&envelope)
		return &apiError{Status: res.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	}
	if output == nil || res.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maximumResponseBytes))
		return nil
	}
	return json.NewDecoder(io.LimitReader(res.Body, maximumResponseBytes)).Decode(output)
}

func (c *apiClient) probeSSE(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, c.base.ResolveReference(&url.URL{Path: "/api/v1/events"}).String(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		return fmt.Errorf("unexpected SSE response %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	buffer := make([]byte, 256)
	for {
		n, readErr := res.Body.Read(buffer)
		if n > 0 {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (c *apiClient) ensureFolder(ctx context.Context, name string) (uuid.UUID, error) {
	var listing struct {
		Items []folder `json:"items"`
	}
	if err := c.json(ctx, http.MethodGet, "/api/v1/asset-folders", nil, &listing, ""); err != nil {
		return uuid.Nil, err
	}
	for _, item := range listing.Items {
		if item.Name == name {
			return item.ID, nil
		}
	}
	var created folder
	if err := c.json(ctx, http.MethodPost, "/api/v1/asset-folders", map[string]string{"name": name}, &created, ""); err != nil {
		return uuid.Nil, err
	}
	return created.ID, nil
}

func (c *apiClient) ensureReference(ctx context.Context, folderID uuid.UUID) (uuid.UUID, error) {
	data, err := referencePNG()
	if err != nil {
		return uuid.Nil, err
	}
	var session uploadState
	if err = c.json(ctx, http.MethodPost, "/api/v1/uploads", map[string]any{"filename": "cornfield-canary-reference.png", "media_type": "image/png", "size": len(data)}, &session, ""); err != nil {
		return uuid.Nil, err
	}
	uploadURL := session.ContentURL
	if uploadURL == "" {
		uploadURL = "/api/v1/uploads/" + session.ID.String() + "/content"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base.ResolveReference(&url.URL{Path: uploadURL}).String(), bytes.NewReader(data))
	if err != nil {
		return uuid.Nil, err
	}
	req.Header.Set("Content-Type", "image/png")
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.http.Do(req)
	if err != nil {
		return uuid.Nil, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, maximumResponseBytes))
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		return uuid.Nil, fmt.Errorf("upload content returned HTTP %d", res.StatusCode)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if err = c.json(ctx, http.MethodGet, "/api/v1/uploads/"+session.ID.String(), nil, &session, ""); err != nil {
			return uuid.Nil, err
		}
		if session.Status == "ready" && session.AssetID != nil {
			if err = c.organize(ctx, *session.AssetID, folderID); err != nil {
				return uuid.Nil, err
			}
			return *session.AssetID, nil
		}
		if session.Status == "failed" || session.Status == "expired" {
			return uuid.Nil, fmt.Errorf("reference upload %s: %v", session.Status, session.ErrorCode)
		}
		time.Sleep(time.Second)
	}
	return uuid.Nil, errors.New("reference upload timed out")
}

func referencePNG() ([]byte, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			canvas.SetRGBA(x, y, color.RGBA{R: uint8(30 + x/8), G: uint8(80 + y/12), B: 48, A: 255})
		}
	}
	var output bytes.Buffer
	err := png.Encode(&output, canvas)
	return output.Bytes(), err
}

func buildTextCases(model modelconfig.Model, revision, _ string, seed int64) []canaryCase {
	ratios := append([]string(nil), model.Capabilities.AspectRatios...)
	if len(ratios) == 0 {
		ratios = []string{"auto"}
	}
	resolutions := append([]string(nil), model.Capabilities.Resolutions...)
	if len(resolutions) == 0 {
		resolutions = []string{"auto"}
	}
	qualities := append([]string(nil), model.Capabilities.Qualities...)
	if len(qualities) == 0 {
		qualities = []string{""}
	} else {
		// Quality models use quality as their sole resolution-like axis.
		resolutions = []string{"auto"}
	}
	cases := make([]canaryCase, 0, len(ratios)*len(resolutions)*len(qualities))
	for _, resolution := range resolutions {
		for _, ratio := range ratios {
			for _, quality := range qualities {
				key := caseKey(model.ID, "text", resolution, ratio, quality)
				prompt := randomPrompt(seed, key, false)
				item := canaryCase{Key: key, Model: model, Revision: revision, Mode: "text", AspectRatio: ratio, Resolution: resolution, Quality: quality, Prompt: prompt, PromptSHA256: hashText(prompt)}
				if overrides := model.SizeOverrides[resolution]; overrides != nil {
					item.ExpectedSize = overrides[ratio]
				}
				cases = append(cases, item)
			}
		}
	}
	return cases
}

func buildImageCase(model modelconfig.Model, revision, _ string, seed int64, referenceID uuid.UUID) canaryCase {
	ratio, resolution, quality := "auto", "auto", ""
	if len(model.Capabilities.AspectRatios) > 0 {
		ratio = model.Capabilities.AspectRatios[0]
	}
	if len(model.Capabilities.Resolutions) > 0 {
		resolution = model.Capabilities.Resolutions[0]
	}
	if len(model.Capabilities.Qualities) > 0 {
		quality = model.Capabilities.Qualities[0]
		resolution = "auto"
	}
	key := caseKey(model.ID, "image", resolution, ratio, quality)
	prompt := randomPrompt(seed, key, true)
	item := canaryCase{Key: key, Model: model, Revision: revision, Mode: "image", AspectRatio: ratio, Resolution: resolution, Quality: quality, ReferenceID: &referenceID, Prompt: prompt, PromptSHA256: hashText(prompt)}
	if overrides := model.SizeOverrides[resolution]; overrides != nil {
		item.ExpectedSize = overrides[ratio]
	}
	return item
}

func runModel(ctx context.Context, client *apiClient, store *reportStore, folderID uuid.UUID, cases []canaryCase) error {
	pending := make([]canaryCase, 0, len(cases))
	for _, item := range cases {
		if !store.passed(item.Key) {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	modelID := pending[0].Model.ID
	modelCtx, cancelModel := context.WithCancel(ctx)
	defer cancelModel()
	jobs := make(chan canaryCase)
	results := make(chan caseResult)
	workerCount := min(4, len(pending))
	var workers sync.WaitGroup
	rate := time.NewTicker(5 * time.Second)
	defer rate.Stop()
	stopRefill := make(chan struct{})
	defer close(stopRefill)
	burst := make(chan struct{}, 4)
	for range 4 {
		burst <- struct{}{}
	}
	go func() {
		for {
			select {
			case <-stopRefill:
				return
			case <-rate.C:
				select {
				case burst <- struct{}{}:
				default:
				}
			}
		}
	}()
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				select {
				case <-modelCtx.Done():
					return
				case <-burst:
				}
				result := client.runCase(modelCtx, folderID, item)
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		for _, item := range pending {
			select {
			case jobs <- item:
			case <-modelCtx.Done():
				close(jobs)
				workers.Wait()
				close(results)
				return
			}
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	failureCounts := make(map[string]int)
	var systemic string
	for result := range results {
		if err := store.upsert(result); err != nil {
			return err
		}
		fmt.Printf("%s %s %s/%s %s (%dms)\n", result.ModelID, result.Mode, result.Resolution, result.AspectRatio, result.Status, result.DurationMS)
		if result.Status != "passed" && result.ErrorCode != "" {
			failureCounts[result.ErrorCode]++
			if failureCounts[result.ErrorCode] >= 3 {
				systemic = result.ErrorCode
				cancelModel()
			}
		}
	}
	if systemic != "" {
		return fmt.Errorf("three failures with %s", systemic)
	}
	for _, item := range cases {
		if !store.passed(item.Key) {
			return fmt.Errorf("model %s has failed or incomplete cases", modelID)
		}
	}
	return nil
}

func (c *apiClient) runCase(ctx context.Context, folderID uuid.UUID, item canaryCase) caseResult {
	started := time.Now().UTC()
	result := caseResult{Key: item.Key, ModelID: item.Model.ID, Mode: item.Mode, AspectRatio: item.AspectRatio, Resolution: item.Resolution, Quality: item.Quality, PromptSHA256: item.PromptSHA256, Status: "failed", ExpectedOutputs: item.Model.OutputsPerDraw, StartedAt: started}
	inputAssets := []uuid.UUID{}
	if item.ReferenceID != nil {
		inputAssets = append(inputAssets, *item.ReferenceID)
	}
	options := provider.GenerationOptions{}
	if len(item.Model.Capabilities.MidjourneyVersions) > 0 {
		options.Midjourney = &provider.MidjourneyOptions{Version: "8.1", Resolution: strings.ToLower(item.Resolution), Speed: "fast", Stylize: 100}
	}
	if item.Quality != "" {
		options.Image = &provider.ImageOptions{Quality: item.Quality}
	}
	payload := map[string]any{
		"model_id": item.Model.ID, "capability_revision": item.Revision, "prompt": item.Prompt,
		"aspect_ratio": item.AspectRatio, "resolution": item.Resolution, "draw_count": 1, "input_asset_ids": inputAssets, "options": options,
	}
	var batch generationBatch
	idempotencyKey := uuid.NewString()
	if err := c.createGeneration(ctx, payload, &batch, idempotencyKey); err != nil {
		result.ErrorCode, result.ErrorMessage = errorFields(err)
		return finishResult(result, started)
	}
	result.BatchID = &batch.ID
	timeout := time.Duration(item.Model.Policy.SubmitTimeoutSeconds+item.Model.Policy.GenerationTimeoutSeconds+120) * time.Second
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for !terminalBatch(batch.Status) {
		select {
		case <-pollCtx.Done():
			result.ErrorCode, result.ErrorMessage = "CANARY_TIMEOUT", pollCtx.Err().Error()
			return finishResult(result, started)
		case <-time.After(3 * time.Second):
		}
		if err := c.json(pollCtx, http.MethodGet, "/api/v1/generations/"+batch.ID.String(), nil, &batch, ""); err != nil {
			result.ErrorCode, result.ErrorMessage = errorFields(err)
			return finishResult(result, started)
		}
	}
	result.CompletedOutputs = batch.CompletedOutputs
	for _, job := range batch.Jobs {
		result.Outputs = append(result.Outputs, job.Outputs...)
		if job.ErrorCode != nil && result.ErrorCode == "" {
			result.ErrorCode = *job.ErrorCode
		}
		if job.ErrorMessage != nil && result.ErrorMessage == "" {
			result.ErrorMessage = bounded(*job.ErrorMessage, 1024)
		}
	}
	if batch.Status != "succeeded" || len(result.Outputs) != item.Model.OutputsPerDraw {
		if result.ErrorCode == "" {
			result.ErrorCode = "CANARY_GENERATION_FAILED"
		}
		if result.ErrorMessage == "" {
			result.ErrorMessage = fmt.Sprintf("batch status %s with %d outputs", batch.Status, len(result.Outputs))
		}
		return finishResult(result, started)
	}
	if item.ExpectedSize != "" {
		for _, output := range result.Outputs {
			if fmt.Sprintf("%dx%d", output.Width, output.Height) != item.ExpectedSize {
				result.ErrorCode = "CANARY_SIZE_MISMATCH"
				result.ErrorMessage = fmt.Sprintf("expected %s, received %dx%d", item.ExpectedSize, output.Width, output.Height)
				return finishResult(result, started)
			}
		}
	} else if !item.Model.PromptAspectRatio && item.AspectRatio != "auto" {
		for _, output := range result.Outputs {
			if !ratioMatches(output.Width, output.Height, item.AspectRatio, 0.05) {
				result.ErrorCode = "CANARY_RATIO_MISMATCH"
				result.ErrorMessage = fmt.Sprintf("received %dx%d for %s", output.Width, output.Height, item.AspectRatio)
				return finishResult(result, started)
			}
		}
	}
	for _, output := range result.Outputs {
		if err := c.organize(ctx, output.AssetID, folderID); err != nil {
			result.ErrorCode, result.ErrorMessage = errorFields(err)
			return finishResult(result, started)
		}
	}
	result.Status = "passed"
	return finishResult(result, started)
}

func (c *apiClient) createGeneration(ctx context.Context, payload any, batch *generationBatch, idempotencyKey string) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		lastErr = c.json(ctx, http.MethodPost, "/api/v1/generations", payload, batch, idempotencyKey)
		if lastErr == nil {
			return nil
		}
		var apiErr *apiError
		if errors.As(lastErr, &apiErr) && apiErr.Status != http.StatusTooManyRequests && apiErr.Status < 500 {
			return lastErr
		}
		delay := time.Duration(1<<attempt) * time.Second
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

func (c *apiClient) organize(ctx context.Context, assetID, folderID uuid.UUID) error {
	return c.json(ctx, http.MethodPatch, "/api/v1/assets/"+assetID.String()+"/organization", map[string]any{"folder_id": folderID, "archived": true}, nil, "")
}

func openReport(path, release, revision, username string) (*reportStore, error) {
	store := &reportStore{path: path, report: report{ReleaseSHA: release, CapabilityRevision: revision, Username: username, StartedAt: time.Now().UTC()}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &store.report); err != nil {
			return nil, err
		}
		if store.report.ReleaseSHA != release || store.report.CapabilityRevision != revision || !strings.EqualFold(store.report.Username, username) {
			return nil, errors.New("existing report belongs to another release, capability revision, or user")
		}
		return store, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return store, store.upsert(caseResult{})
}

func (s *reportStore) passed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range s.report.Results {
		if result.Key == key && result.Status == "passed" {
			return true
		}
	}
	return false
}

func (s *reportStore) allPassed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.report.Results) == 0 {
		return false
	}
	for _, result := range s.report.Results {
		if result.Key != "" && result.Status != "passed" {
			return false
		}
	}
	return true
}

func (s *reportStore) upsert(result caseResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if result.Key != "" {
		updated := false
		for index := range s.report.Results {
			if s.report.Results[index].Key == result.Key {
				s.report.Results[index] = result
				updated = true
				break
			}
		}
		if !updated {
			s.report.Results = append(s.report.Results, result)
		}
		sort.Slice(s.report.Results, func(i, j int) bool { return s.report.Results[i].Key < s.report.Results[j].Key })
	}
	s.report.UpdatedAt = time.Now().UTC()
	return s.writeLocked()
}

func (s *reportStore) writeLocked() error {
	data, err := json.MarshalIndent(s.report, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	if err = os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".canary-report-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryName, s.path)
}

func finishResult(result caseResult, started time.Time) caseResult {
	result.CompletedAt = time.Now().UTC()
	result.DurationMS = result.CompletedAt.Sub(started).Milliseconds()
	result.ErrorMessage = bounded(result.ErrorMessage, 1024)
	return result
}

func terminalBatch(status string) bool {
	switch status {
	case "succeeded", "partial", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func ratioMatches(width, height int, ratio string, tolerance float64) bool {
	parts := strings.Split(ratio, ":")
	if width < 1 || height < 1 || len(parts) != 2 {
		return false
	}
	var numerator, denominator float64
	if _, err := fmt.Sscanf(parts[0], "%f", &numerator); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(parts[1], "%f", &denominator); err != nil || denominator == 0 {
		return false
	}
	expected := numerator / denominator
	return math.Abs(float64(width)/float64(height)-expected)/expected <= tolerance
}

func randomPrompt(seed int64, key string, imageMode bool) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, key)))
	random := rand.New(rand.NewSource(int64FromBytes(hash[:8])))
	subjects := []string{"a cobalt glass sphere", "a quiet observatory", "a sculptural red chair", "a small greenhouse", "a paper spacecraft", "a ceramic fox"}
	settings := []string{"in a sunlit cornfield", "on a neutral studio cyclorama", "beside a calm alpine lake", "inside a minimal concrete gallery", "under a clear twilight sky"}
	styles := []string{"editorial photography", "cinematic realism", "refined product photography", "architectural visualization", "soft analog film"}
	prompt := fmt.Sprintf("Cornfield release canary. %s %s, %s, balanced composition, no text, no logos.", subjects[random.Intn(len(subjects))], settings[random.Intn(len(settings))], styles[random.Intn(len(styles))])
	if imageMode {
		prompt += " Preserve the reference image's color character while creating a clearly different composition."
	}
	return prompt
}

func int64FromBytes(data []byte) int64 {
	var value uint64
	for _, item := range data {
		value = value<<8 | uint64(item)
	}
	return int64(value)
}

func deterministicSeed(release string) int64 {
	hash := sha256.Sum256([]byte(release))
	return int64FromBytes(hash[:8])
}

func caseKey(model, mode, resolution, ratio, quality string) string {
	return strings.Join([]string{model, mode, resolution, ratio, quality}, "|")
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func errorFields(err error) (string, string) {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Code, bounded(apiErr.Message, 1024)
	}
	return "CANARY_CLIENT_ERROR", bounded(err.Error(), 1024)
}

func bounded(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum-3] + "..."
}
