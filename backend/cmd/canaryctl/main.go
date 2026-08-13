package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	stdDraw "image/draw"
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
	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/safehttp"
)

const (
	defaultUsername         = "Intern2"
	maximumResponseBytes    = 4 << 20
	referenceUploadInterval = 3 * time.Second
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

type assetPresentation struct {
	ID          uuid.UUID `json:"id"`
	BlurDataURL string    `json:"blur_data_url"`
	Thumb320URL string    `json:"thumb_320_url"`
	Thumb640URL string    `json:"thumb_640_url"`
}

type editorDocument struct {
	SchemaVersion int            `json:"schema_version"`
	Canvas        editorCanvas   `json:"canvas"`
	Objects       []editorObject `json:"objects"`
}

type editorCanvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type editorCrop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type editorObject struct {
	ID        string      `json:"id"`
	AssetID   uuid.UUID   `json:"asset_id"`
	Transform [6]float64  `json:"transform"`
	Opacity   float64     `json:"opacity"`
	Visible   bool        `json:"visible"`
	Locked    bool        `json:"locked"`
	ZIndex    int         `json:"z_index"`
	Crop      *editorCrop `json:"crop,omitempty"`
}

type editorProject struct {
	ID            uuid.UUID      `json:"id"`
	SourceAssetID uuid.UUID      `json:"source_asset_id"`
	Document      editorDocument `json:"document"`
	Revision      int64          `json:"revision"`
}

type layerAsset struct {
	ID          uuid.UUID `json:"id"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	BlurDataURL string    `json:"blur_data_url"`
}

type layerSetItem struct {
	ID                    uuid.UUID  `json:"id"`
	ZIndex                int        `json:"z_index"`
	Name                  string     `json:"name"`
	BoundingBoxAbsolute   []int      `json:"bounding_box_absolute"`
	BoundingBoxNormalized []float64  `json:"bounding_box_normalized"`
	Asset                 layerAsset `json:"asset"`
}

type layerSet struct {
	ID           uuid.UUID      `json:"id"`
	BaseAsset    layerAsset     `json:"base_asset"`
	Items        []layerSetItem `json:"items"`
	PackageReady bool           `json:"package_ready"`
}

type assetOperation struct {
	ID                  uuid.UUID  `json:"id"`
	Status              string     `json:"status"`
	SourceRevision      int64      `json:"source_revision"`
	SubmissionUncertain bool       `json:"submission_uncertain"`
	ErrorCode           *string    `json:"error_code"`
	ErrorMessage        *string    `json:"error_message"`
	ResultAssetID       *uuid.UUID `json:"result_asset_id"`
	LayerSet            *layerSet  `json:"layer_set"`
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
	Key                    string
	Model                  modelconfig.Model
	Revision               string
	Mode                   string
	AspectRatio            string
	Resolution             string
	Quality                string
	PromptOptimizationMode string
	ReferenceIDs           []uuid.UUID
	ExpectedSize           string
	Prompt                 string
	PromptSHA256           string
	Midjourney             *provider.MidjourneyOptions
}

type caseResult struct {
	Key                    string             `json:"key"`
	ModelID                string             `json:"model_id"`
	Mode                   string             `json:"mode"`
	AspectRatio            string             `json:"aspect_ratio"`
	Resolution             string             `json:"resolution"`
	Quality                string             `json:"quality,omitempty"`
	PromptOptimizationMode string             `json:"prompt_optimization_mode,omitempty"`
	PromptSHA256           string             `json:"prompt_sha256"`
	BatchID                *uuid.UUID         `json:"batch_id,omitempty"`
	Status                 string             `json:"status"`
	ErrorCode              string             `json:"error_code,omitempty"`
	ErrorMessage           string             `json:"error_message,omitempty"`
	ExpectedOutputs        int                `json:"expected_outputs"`
	CompletedOutputs       int                `json:"completed_outputs"`
	Outputs                []generationOutput `json:"outputs,omitempty"`
	DurationMS             int64              `json:"duration_ms"`
	StartedAt              time.Time          `json:"started_at"`
	CompletedAt            time.Time          `json:"completed_at"`
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
	var baseURL, username, passwordFile, promptFile, providerKeyFile, artifactDir, releaseSHA, configPath, reportPath, profile string
	var allowHTTP bool
	var archiveOutput bool
	flag.StringVar(&baseURL, "base-url", "https://corn.kumadrama.com", "Cornfield HTTPS origin")
	flag.StringVar(&username, "username", defaultUsername, "existing canary username")
	flag.StringVar(&passwordFile, "password-file", "", "root-managed file containing the canary password")
	flag.StringVar(&promptFile, "prompt-file", "", "optional file containing one prompt for every canary case")
	flag.StringVar(&providerKeyFile, "provider-key-file", "", "root-managed BytePlus API key file for layer-protocol")
	flag.StringVar(&artifactDir, "artifact-dir", "", "private output directory for layer protocol artifacts")
	flag.StringVar(&releaseSHA, "release", "", "deployed release commit SHA")
	flag.StringVar(&configPath, "model-config", "./config/models.yaml", "deployed model catalog")
	flag.StringVar(&reportPath, "report", "", "resumable JSON report path")
	flag.StringVar(&profile, "profile", "matrix", "canary profile: matrix, launch, byteplus, layer-protocol, or layer-e2e")
	flag.BoolVar(&archiveOutput, "archive-output", true, "archive generated canary assets")
	flag.BoolVar(&allowHTTP, "allow-http", false, "allow HTTP for isolated tests only")
	flag.Parse()
	if profile != "matrix" && profile != "launch" && profile != "byteplus" && profile != "layer-protocol" && profile != "layer-e2e" {
		return errors.New("--profile must be matrix, launch, byteplus, layer-protocol, or layer-e2e")
	}
	if profile == "layer-protocol" {
		if providerKeyFile == "" || reportPath == "" || artifactDir == "" {
			return errors.New("layer-protocol requires --provider-key-file, --report, and --artifact-dir")
		}
		return runLayerProtocol(providerKeyFile, reportPath, artifactDir)
	}

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
	if profile == "layer-e2e" {
		if reportPath == "" {
			reportPath = "layer-e2e-" + shortSHA(releaseSHA) + ".json"
		}
		return runLayerE2E(ctx, client, reportPath, releaseSHA, catalog.Hash, username)
	}

	folderID, err := client.ensureFolder(ctx, "Canary "+shortSHA(releaseSHA))
	if err != nil {
		return fmt.Errorf("create canary folder: %w", err)
	}
	referenceCount := 1
	if profile == "byteplus" {
		referenceCount = 10
	}
	referenceIDs := make([]uuid.UUID, 0, referenceCount)
	referencePermits := newCreatePermitStream(ctx, referenceUploadInterval)
	for index := 0; index < referenceCount; index++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-referencePermits:
		}
		referenceID, referenceErr := client.ensureReference(ctx, folderID)
		if referenceErr != nil {
			return fmt.Errorf("prepare reference image %d: %w", index+1, referenceErr)
		}
		referenceIDs = append(referenceIDs, referenceID)
	}
	store, err := openReport(reportPath, releaseSHA, catalog.Hash, username)
	if err != nil {
		return fmt.Errorf("open report: %w", err)
	}

	seed := deterministicSeed(releaseSHA)
	allPassed := true
	createPermits := newCreatePermitStream(ctx, 5*time.Second)
	caseGroups := buildCanaryGroups(catalog, profile, releaseSHA, seed, referenceIDs)
	if promptFile != "" {
		prompt, promptErr := readPrompt(promptFile)
		if promptErr != nil {
			return fmt.Errorf("read prompt: %w", promptErr)
		}
		applyPromptOverride(caseGroups, prompt)
	}
	for _, cases := range caseGroups {
		if err := runModel(ctx, client, store, folderID, archiveOutput, cases, createPermits); err != nil {
			allPassed = false
			fmt.Fprintf(os.Stderr, "model %s paused: %v\n", cases[0].Model.ID, err)
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

func buildCanaryGroups(catalog *modelconfig.Catalog, profile, release string, seed int64, referenceIDs []uuid.UUID) [][]canaryCase {
	groups := make([][]canaryCase, 0)
	for _, model := range catalog.Models {
		if !model.Enabled {
			continue
		}
		if profile == "byteplus" {
			if model.ID == "byteplus-seedream-5-0-pro" {
				groups = append(groups, buildBytePlusCases(model, catalog.Hash, release, seed, referenceIDs))
			}
			continue
		}
		if profile == "launch" {
			var cases []canaryCase
			switch model.ID {
			case "legnext-midjourney":
				cases = buildLaunchMidjourneyCases(model, catalog.Hash, seed, referenceIDs[0])
			case "openrouter-gemini-3-1-flash-image", "bfl-flux-2-max":
				text := buildTextCases(model, catalog.Hash, release, seed)
				if len(text) > 0 {
					cases = append(cases, text[0])
				}
				cases = append(cases, buildImageCase(model, catalog.Hash, release, seed, referenceIDs[0]))
			}
			if len(cases) > 0 {
				groups = append(groups, cases)
			}
			continue
		}
		cases := buildTextCases(model, catalog.Hash, release, seed)
		if model.Capabilities.ImageToImage {
			cases = append(cases, buildImageCase(model, catalog.Hash, release, seed, referenceIDs[0]))
		}
		groups = append(groups, cases)
	}
	return groups
}

func buildLaunchMidjourneyCases(model modelconfig.Model, revision string, seed int64, referenceID uuid.UUID) []canaryCase {
	makeCase := func(name, resolution, ratio, prompt string, reference *uuid.UUID, raw, tile bool) canaryCase {
		key := "launch|" + name
		item := canaryCase{
			Key: key, Model: model, Revision: revision, Mode: map[bool]string{true: "image", false: "text"}[reference != nil],
			AspectRatio: ratio, Resolution: resolution, Prompt: prompt, PromptSHA256: hashText(prompt),
			Midjourney: &provider.MidjourneyOptions{Version: "8.2", Resolution: strings.ToLower(resolution), Speed: "fast", Stylize: 100, Raw: raw, Tile: tile},
		}
		if reference != nil {
			item.ReferenceIDs = []uuid.UUID{*reference}
		}
		return item
	}
	cases := make([]canaryCase, 0, 20)
	for _, resolution := range []string{"SD", "HD"} {
		for _, ratio := range model.Capabilities.AspectRatios {
			name := strings.ToLower(resolution) + "-" + strings.ReplaceAll(ratio, ":", "x")
			cases = append(cases, makeCase(name, resolution, ratio, randomPrompt(seed, name, false), nil, false, false))
		}
		name := strings.ToLower(resolution) + "-image"
		cases = append(cases, makeCase(name, resolution, "1:1", randomPrompt(seed, name, true), &referenceID, false, false))
	}
	cases = append(cases,
		makeCase("chinese", "SD", "1:1", "雨后的玉米地，远处有一座极简白色观测站，电影光影", nil, false, false),
		makeCase("structured", "SD", "16:9", "Cinematic editorial photograph of a quiet agricultural research station at blue hour, layered foreground crops, restrained amber practical lights, deep atmospheric perspective, realistic materials and subtle film grain", nil, false, false),
		makeCase("raw", "SD", "3:2", randomPrompt(seed, "raw", false), nil, true, false),
		makeCase("tile", "SD", "1:1", "seamless geometric corn leaf pattern, restrained green and warm gold", nil, false, true),
	)
	return cases
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

func readPrompt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return "", errors.New("prompt file is empty")
	}
	return prompt, nil
}

func applyPromptOverride(groups [][]canaryCase, prompt string) {
	for groupIndex := range groups {
		for caseIndex := range groups[groupIndex] {
			item := &groups[groupIndex][caseIndex]
			item.Prompt = prompt
			if item.Mode == "image" {
				item.Prompt += " Preserve the reference image's character identity and visual design."
			}
			item.PromptSHA256 = hashText(item.Prompt)
		}
	}
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
	cases := make([]canaryCase, 0, len(model.Capabilities.AspectRatios)*len(resolutions)*len(qualities))
	for _, resolution := range resolutions {
		ratios := append([]string(nil), model.AspectRatiosForResolution(resolution)...)
		if len(ratios) == 0 {
			ratios = []string{"auto"}
		}
		for _, ratio := range ratios {
			for _, quality := range qualities {
				key := caseKey(model.ID, "text", resolution, ratio, quality)
				prompt := randomPrompt(seed, key, false)
				item := canaryCase{Key: key, Model: model, Revision: revision, Mode: "text", AspectRatio: ratio, Resolution: resolution, Quality: quality, Prompt: prompt, PromptSHA256: hashText(prompt)}
				if modes := model.Capabilities.PromptOptimizationModes; len(modes) > 0 {
					item.PromptOptimizationMode = modes[len(cases)%len(modes)]
					item.Key += "|" + item.PromptOptimizationMode
				}
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
	item := canaryCase{Key: key, Model: model, Revision: revision, Mode: "image", AspectRatio: ratio, Resolution: resolution, Quality: quality, ReferenceIDs: []uuid.UUID{referenceID}, Prompt: prompt, PromptSHA256: hashText(prompt)}
	if modes := model.Capabilities.PromptOptimizationModes; len(modes) > 0 {
		item.PromptOptimizationMode = modes[0]
		item.Key += "|" + item.PromptOptimizationMode
	}
	if overrides := model.SizeOverrides[resolution]; overrides != nil {
		item.ExpectedSize = overrides[ratio]
	}
	return item
}

func buildBytePlusCases(model modelconfig.Model, revision, release string, seed int64, referenceIDs []uuid.UUID) []canaryCase {
	cases := buildTextCases(model, revision, release, seed)
	for _, input := range []struct {
		name       string
		resolution string
		ratio      string
		mode       string
		refs       int
	}{
		{name: "one-reference", resolution: "1K", ratio: "1:1", mode: "standard", refs: 1},
		{name: "two-references", resolution: "1.5K", ratio: "4:3", mode: "fast", refs: 2},
		{name: "ten-references", resolution: "2K", ratio: "16:9", mode: "standard", refs: 10},
	} {
		key := "byteplus|image|" + input.name
		prompt := randomPrompt(seed, key, true)
		cases = append(cases, canaryCase{
			Key: key, Model: model, Revision: revision, Mode: "image", AspectRatio: input.ratio,
			Resolution: input.resolution, PromptOptimizationMode: input.mode,
			ReferenceIDs: append([]uuid.UUID(nil), referenceIDs[:input.refs]...),
			ExpectedSize: model.SizeOverrides[input.resolution][input.ratio], Prompt: prompt, PromptSHA256: hashText(prompt),
		})
	}
	return cases
}

func newCreatePermitStream(ctx context.Context, interval time.Duration) <-chan struct{} {
	permits := make(chan struct{})
	go func() {
		defer close(permits)
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			select {
			case <-ctx.Done():
				return
			case permits <- struct{}{}:
				timer.Reset(interval)
			}
		}
	}()
	return permits
}

func runModel(ctx context.Context, client *apiClient, store *reportStore, folderID uuid.UUID, archiveOutput bool, cases []canaryCase, createPermit <-chan struct{}) error {
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
	workerCount := min(4, pending[0].Model.Policy.MaxConcurrency, len(pending))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				select {
				case <-modelCtx.Done():
					return
				case <-createPermit:
				}
				result := client.runCase(modelCtx, folderID, archiveOutput, item)
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

func (c *apiClient) runCase(ctx context.Context, folderID uuid.UUID, archiveOutput bool, item canaryCase) caseResult {
	started := time.Now().UTC()
	result := caseResult{Key: item.Key, ModelID: item.Model.ID, Mode: item.Mode, AspectRatio: item.AspectRatio, Resolution: item.Resolution, Quality: item.Quality, PromptOptimizationMode: item.PromptOptimizationMode, PromptSHA256: item.PromptSHA256, Status: "failed", ExpectedOutputs: item.Model.OutputsPerDraw, StartedAt: started}
	inputAssets := append([]uuid.UUID(nil), item.ReferenceIDs...)
	options := provider.GenerationOptions{}
	if len(item.Model.Capabilities.MidjourneyVersions) > 0 {
		options.Midjourney = item.Midjourney
		if options.Midjourney == nil {
			options.Midjourney = &provider.MidjourneyOptions{Version: "8.2", Resolution: strings.ToLower(item.Resolution), Speed: "fast", Stylize: 100}
		}
	}
	if item.Quality != "" {
		options.Image = &provider.ImageOptions{Quality: item.Quality}
	}
	if item.PromptOptimizationMode != "" {
		if options.Image == nil {
			options.Image = &provider.ImageOptions{}
		}
		options.Image.PromptOptimizationMode = item.PromptOptimizationMode
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
		if err := c.validateAssetPresentation(ctx, output.AssetID); err != nil {
			result.ErrorCode, result.ErrorMessage = "CANARY_PRESENTATION_INVALID", err.Error()
			return finishResult(result, started)
		}
		if err := c.organize(ctx, output.AssetID, folderID, archiveOutput); err != nil {
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
		if errors.As(lastErr, &apiErr) && apiErr.Status == http.StatusTooManyRequests {
			delay = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

func (c *apiClient) organize(ctx context.Context, assetID, folderID uuid.UUID, archived ...bool) error {
	archive := true
	if len(archived) > 0 {
		archive = archived[0]
	}
	return c.json(ctx, http.MethodPatch, "/api/v1/assets/"+assetID.String()+"/organization", map[string]any{"folder_id": folderID, "archived": archive}, nil, "")
}

func (c *apiClient) validateAssetPresentation(ctx context.Context, assetID uuid.UUID) error {
	var asset assetPresentation
	if err := c.json(ctx, http.MethodGet, "/api/v1/assets/"+assetID.String(), nil, &asset, ""); err != nil {
		return err
	}
	if asset.BlurDataURL == "" || len(asset.BlurDataURL) > 4096 {
		return fmt.Errorf("blur placeholder has invalid size %d", len(asset.BlurDataURL))
	}
	for _, variant := range []string{"320", "640"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.ResolveReference(&url.URL{Path: "/api/v1/assets/" + assetID.String() + "/content", RawQuery: "variant=" + variant}).String(), nil)
		if err != nil {
			return err
		}
		res, err := c.http.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/webp" || strings.HasPrefix(res.Header.Get("X-Cornfield-Variant"), "fallback-") {
			return fmt.Errorf("variant %s returned HTTP %d %q fallback=%q", variant, res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("X-Cornfield-Variant"))
		}
	}
	return nil
}

type layerE2ECase struct {
	Name      string
	Size      string
	Mode      string
	Prompt    string
	Transform bool
}

type layerE2EResult struct {
	Name            string     `json:"name"`
	Size            string     `json:"size"`
	Mode            string     `json:"prompt_optimization_mode"`
	Status          string     `json:"status"`
	ErrorCode       string     `json:"error_code,omitempty"`
	ErrorMessage    string     `json:"error_message,omitempty"`
	ProjectID       *uuid.UUID `json:"project_id,omitempty"`
	OperationID     *uuid.UUID `json:"operation_id,omitempty"`
	LayerSetID      *uuid.UUID `json:"layer_set_id,omitempty"`
	LayerCount      int        `json:"layer_count,omitempty"`
	DurationMS      int64      `json:"duration_ms"`
	SSELatencyMS    int64      `json:"sse_latency_ms,omitempty"`
	SnapshotVariant bool       `json:"required_variants_ready"`
}

type layerE2EReport struct {
	ReleaseSHA         string           `json:"release_sha"`
	CapabilityRevision string           `json:"capability_revision"`
	Username           string           `json:"username"`
	StartedAt          time.Time        `json:"started_at"`
	CompletedAt        *time.Time       `json:"completed_at,omitempty"`
	Results            []layerE2EResult `json:"results"`
}

func runLayerE2E(ctx context.Context, client *apiClient, reportPath, release, revision, username string) error {
	report := layerE2EReport{ReleaseSHA: release, CapabilityRevision: revision, Username: username, StartedAt: time.Now().UTC()}
	folderID, err := client.ensureFolder(ctx, "Canary Layers "+shortSHA(release))
	if err != nil {
		return fmt.Errorf("create layer canary folder: %w", err)
	}
	cases := []layerE2ECase{
		{Name: "auto-standard", Size: "auto", Mode: "standard"},
		{Name: "1k-fast", Size: "1K", Mode: "fast"},
		{Name: "1.5k-standard", Size: "1.5K", Mode: "standard"},
		{Name: "2k-standard", Size: "2K", Mode: "standard"},
		{Name: "elements-1k", Size: "1K", Mode: "standard", Prompt: "Separate the central figure, title, lime circle, blue rectangle, and background into independent layers."},
		{Name: "transformed-bbox-2k", Size: "2K", Mode: "fast", Transform: true, Prompt: "Separate the central figure inside <bbox>350 160 650 880</bbox>, the title, both geometric shapes, and the background."},
	}
	for _, item := range cases {
		result := client.runLayerE2ECase(ctx, folderID, item)
		report.Results = append(report.Results, result)
		if err = writePrivateJSON(reportPath, report); err != nil {
			return err
		}
		fmt.Printf("layer-e2e %s %s (%dms)\n", item.Name, result.Status, result.DurationMS)
		if result.Status != "passed" {
			return fmt.Errorf("layer e2e stopped after %s: %s %s", item.Name, result.ErrorCode, result.ErrorMessage)
		}
	}
	now := time.Now().UTC()
	report.CompletedAt = &now
	if err = writePrivateJSON(reportPath, report); err != nil {
		return err
	}
	return nil
}

func (c *apiClient) runLayerE2ECase(ctx context.Context, folderID uuid.UUID, item layerE2ECase) layerE2EResult {
	started := time.Now()
	result := layerE2EResult{Name: item.Name, Size: item.Size, Mode: item.Mode, Status: "failed"}
	fail := func(err error) layerE2EResult {
		result.ErrorCode, result.ErrorMessage = errorFields(err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	poster, err := layerProtocolPoster()
	if err != nil {
		return fail(err)
	}
	assetID, err := c.uploadPNG(ctx, folderID, "layer-canary-"+item.Name+".png", poster)
	if err != nil {
		return fail(err)
	}
	var project editorProject
	if err = c.json(ctx, http.MethodPost, "/api/v1/assets/"+assetID.String()+"/editor-project", nil, &project, ""); err != nil {
		return fail(err)
	}
	result.ProjectID = &project.ID
	if item.Transform {
		angle, scale := 8*math.Pi/180, 0.86
		a, b := math.Cos(angle)*scale, math.Sin(angle)*scale
		cc, d := -math.Sin(angle)*scale, math.Cos(angle)*scale
		cx, cy := float64(project.Document.Canvas.Width)/2, float64(project.Document.Canvas.Height)/2
		project.Document.Objects[0].Transform = [6]float64{a, b, cc, d, cx - a*cx - cc*cy + 70, cy - b*cx - d*cy - 45}
		project.Document.Objects[0].Crop = &editorCrop{X: 0.06, Y: 0.08, Width: 0.88, Height: 0.82}
		var saved struct {
			Revision int64 `json:"revision"`
		}
		if err = c.json(ctx, http.MethodPut, "/api/v1/editor-projects/"+project.ID.String()+"/document", map[string]any{"expected_revision": project.Revision, "document": project.Document}, &saved, ""); err != nil {
			return fail(err)
		}
		project.Revision = saved.Revision
	}
	var created struct {
		ID uuid.UUID `json:"id"`
	}
	operationStarted := time.Now()
	if err = c.json(ctx, http.MethodPost, "/api/v1/editor-projects/"+project.ID.String()+"/layer-decompositions", map[string]any{
		"expected_revision": project.Revision, "prompt": item.Prompt, "resolution": item.Size, "prompt_optimization_mode": item.Mode,
	}, &created, uuid.NewString()); err != nil {
		return fail(err)
	}
	result.OperationID = &created.ID
	sse := make(chan time.Duration, 1)
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	go func() {
		if latency, eventErr := c.waitForOperationEvent(operationCtx, created.ID, operationStarted); eventErr == nil {
			sse <- latency
		}
	}()
	operation, err := c.waitForOperation(operationCtx, created.ID)
	if err != nil {
		return fail(err)
	}
	select {
	case latency := <-sse:
		result.SSELatencyMS = latency.Milliseconds()
	case <-time.After(3 * time.Second):
		return fail(errors.New("terminal SSE event was not observed"))
	}
	if operation.SubmissionUncertain || operation.Status != "succeeded" || operation.LayerSet == nil {
		return fail(fmt.Errorf("operation %s: %v %v", operation.Status, operation.ErrorCode, operation.ErrorMessage))
	}
	if err = c.validateLayerSet(ctx, operation.LayerSet); err != nil {
		return fail(err)
	}
	result.LayerSetID = &operation.LayerSet.ID
	result.LayerCount = len(operation.LayerSet.Items)
	result.SnapshotVariant = true

	// Exercise each publish path once while keeping the other paid cases focused.
	if item.Name == "auto-standard" {
		var published assetPresentation
		if err = c.json(ctx, http.MethodPost, "/api/v1/layer-sets/"+operation.LayerSet.ID.String()+"/items/"+operation.LayerSet.Items[0].ID.String()+"/publish", nil, &published, ""); err != nil {
			return fail(err)
		}
		if err = c.validateAssetPresentation(ctx, published.ID); err != nil {
			return fail(err)
		}
		if err = c.organize(ctx, published.ID, folderID, true); err != nil {
			return fail(err)
		}
		if err = c.createAndValidateLayerPackage(ctx, operation.LayerSet.ID); err != nil {
			return fail(err)
		}
	}
	if item.Name == "2k-standard" {
		project.Document = documentFromLayerSet(*operation.LayerSet)
		var saved struct {
			Revision int64 `json:"revision"`
		}
		if err = c.json(ctx, http.MethodPut, "/api/v1/editor-projects/"+project.ID.String()+"/document", map[string]any{"expected_revision": project.Revision, "document": project.Document}, &saved, ""); err != nil {
			return fail(err)
		}
		var publish struct {
			ID uuid.UUID `json:"id"`
		}
		if err = c.json(ctx, http.MethodPost, "/api/v1/editor-projects/"+project.ID.String()+"/publish", map[string]any{"expected_revision": saved.Revision}, &publish, uuid.NewString()); err != nil {
			return fail(err)
		}
		publishedOperation, waitErr := c.waitForOperation(operationCtx, publish.ID)
		if waitErr != nil || publishedOperation.ResultAssetID == nil {
			if waitErr != nil {
				return fail(waitErr)
			}
			return fail(errors.New("editor publish completed without an asset"))
		}
		if err = c.validateAssetPresentation(ctx, *publishedOperation.ResultAssetID); err != nil {
			return fail(err)
		}
		if err = c.organize(ctx, *publishedOperation.ResultAssetID, folderID, true); err != nil {
			return fail(err)
		}
	}
	result.Status = "passed"
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

func (c *apiClient) uploadPNG(ctx context.Context, folderID uuid.UUID, filename string, data []byte) (uuid.UUID, error) {
	var session uploadState
	if err := c.json(ctx, http.MethodPost, "/api/v1/uploads", map[string]any{"filename": filename, "media_type": "image/png", "size": len(data)}, &session, ""); err != nil {
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
		return uuid.Nil, fmt.Errorf("upload returned HTTP %d", res.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if err = c.json(ctx, http.MethodGet, "/api/v1/uploads/"+session.ID.String(), nil, &session, ""); err != nil {
			return uuid.Nil, err
		}
		if session.Status == "ready" && session.AssetID != nil {
			if err = c.organize(ctx, *session.AssetID, folderID, true); err != nil {
				return uuid.Nil, err
			}
			return *session.AssetID, c.validateAssetPresentation(ctx, *session.AssetID)
		}
		if session.Status == "failed" || session.Status == "expired" {
			return uuid.Nil, fmt.Errorf("upload %s: %v", session.Status, session.ErrorCode)
		}
		time.Sleep(time.Second)
	}
	return uuid.Nil, errors.New("upload timed out")
}

func (c *apiClient) waitForOperation(ctx context.Context, id uuid.UUID) (assetOperation, error) {
	for {
		var operation assetOperation
		if err := c.json(ctx, http.MethodGet, "/api/v1/asset-operations/"+id.String(), nil, &operation, ""); err != nil {
			return operation, err
		}
		switch operation.Status {
		case "succeeded":
			return operation, nil
		case "failed", "cancelled", "submission_uncertain":
			return operation, fmt.Errorf("operation %s: %v %v", operation.Status, operation.ErrorCode, operation.ErrorMessage)
		}
		select {
		case <-ctx.Done():
			return operation, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *apiClient) waitForOperationEvent(ctx context.Context, operationID uuid.UUID, started time.Time) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.ResolveReference(&url.URL{Path: "/api/v1/events"}).String(), nil)
	if err != nil {
		return 0, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("SSE returned HTTP %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 4096), maximumResponseBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var envelope struct {
			Payload struct {
				ID     uuid.UUID `json:"id"`
				Status string    `json:"status"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &envelope) != nil || envelope.Payload.ID != operationID {
			continue
		}
		if envelope.Payload.Status == "succeeded" || envelope.Payload.Status == "failed" || envelope.Payload.Status == "submission_uncertain" || envelope.Payload.Status == "cancelled" {
			return time.Since(started), nil
		}
	}
	return 0, scanner.Err()
}

func (c *apiClient) validateLayerSet(ctx context.Context, set *layerSet) error {
	if set.ID == uuid.Nil || len(set.Items) < 1 || len(set.Items) > 16 {
		return errors.New("layer set has invalid output count")
	}
	if err := c.validateAssetPresentation(ctx, set.BaseAsset.ID); err != nil {
		return fmt.Errorf("base presentation: %w", err)
	}
	seen := map[int]struct{}{0: {}}
	for _, item := range set.Items {
		if item.ID == uuid.Nil || item.ZIndex < 1 || item.Name == "" {
			return errors.New("layer item metadata is invalid")
		}
		if _, exists := seen[item.ZIndex]; exists {
			return errors.New("layer z-index is duplicated")
		}
		seen[item.ZIndex] = struct{}{}
		if len(item.BoundingBoxAbsolute) != 4 || len(item.BoundingBoxNormalized) != 4 {
			return errors.New("layer bounding box is invalid")
		}
		left, top, right, bottom := item.BoundingBoxAbsolute[0], item.BoundingBoxAbsolute[1], item.BoundingBoxAbsolute[2], item.BoundingBoxAbsolute[3]
		if left < 0 || top < 0 || right <= left || bottom <= top || right > set.BaseAsset.Width || bottom > set.BaseAsset.Height {
			return errors.New("layer absolute bounding box is out of bounds")
		}
		for _, value := range item.BoundingBoxNormalized {
			if value < 0 || value > 1000 {
				return errors.New("layer normalized bounding box is out of bounds")
			}
		}
		if err := c.validateAssetPresentation(ctx, item.Asset.ID); err != nil {
			return fmt.Errorf("layer %d presentation: %w", item.ZIndex, err)
		}
	}
	return nil
}

func documentFromLayerSet(set layerSet) editorDocument {
	document := editorDocument{SchemaVersion: 1, Canvas: editorCanvas{Width: set.BaseAsset.Width, Height: set.BaseAsset.Height}}
	document.Objects = append(document.Objects, editorObject{ID: "base-" + set.ID.String(), AssetID: set.BaseAsset.ID, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: true, ZIndex: 0})
	for _, item := range set.Items {
		left, top, right, bottom := item.BoundingBoxAbsolute[0], item.BoundingBoxAbsolute[1], item.BoundingBoxAbsolute[2], item.BoundingBoxAbsolute[3]
		document.Objects = append(document.Objects, editorObject{ID: item.ID.String(), AssetID: item.Asset.ID, Transform: [6]float64{float64(right-left) / float64(item.Asset.Width), 0, 0, float64(bottom-top) / float64(item.Asset.Height), float64(left), float64(top)}, Opacity: 1, Visible: true, ZIndex: item.ZIndex})
	}
	return document
}

func (c *apiClient) createAndValidateLayerPackage(ctx context.Context, layerSetID uuid.UUID) error {
	var created struct {
		ID         uuid.UUID `json:"id"`
		Status     string    `json:"status"`
		ContentURL string    `json:"content_url"`
	}
	if err := c.json(ctx, http.MethodPost, "/api/v1/layer-sets/"+layerSetID.String()+"/package", nil, &created, uuid.NewString()); err != nil {
		return err
	}
	if created.Status != "succeeded" {
		operation, err := c.waitForOperation(ctx, created.ID)
		if err != nil || operation.Status != "succeeded" {
			if err != nil {
				return err
			}
			return errors.New("layer package did not succeed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.ResolveReference(&url.URL{Path: "/api/v1/layer-sets/" + layerSetID.String() + "/package/content"}).String(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "application/zip" {
		return fmt.Errorf("layer package returned HTTP %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 513<<20))
	return err
}

func writePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".layer-e2e-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
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
	return os.Rename(name, path)
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

type layerProtocolCase struct {
	Name   string
	Prompt string
	Size   string
	Mode   string
}

type layerProtocolResult struct {
	Name              string         `json:"name"`
	Size              string         `json:"size"`
	Mode              string         `json:"prompt_optimization_mode"`
	Status            string         `json:"status"`
	ErrorCode         string         `json:"error_code,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	DurationMS        int64          `json:"duration_ms"`
	ProviderRequestID string         `json:"provider_request_id,omitempty"`
	OutputCount       int            `json:"output_count,omitempty"`
	OutputSizes       []string       `json:"output_sizes,omitempty"`
	OutputSHA256      []string       `json:"output_sha256,omitempty"`
	OutputHosts       []string       `json:"output_hosts,omitempty"`
	Usage             map[string]any `json:"usage,omitempty"`
}

type layerProtocolReport struct {
	Model       string                `json:"model"`
	Endpoint    string                `json:"endpoint"`
	StartedAt   time.Time             `json:"started_at"`
	CompletedAt time.Time             `json:"completed_at"`
	Results     []layerProtocolResult `json:"results"`
}

func runLayerProtocol(keyFile, reportPath, artifactDir string) error {
	key, err := readSecretFile(keyFile)
	if err != nil {
		return fmt.Errorf("read BytePlus API key: %w", err)
	}
	if err = os.MkdirAll(artifactDir, 0o700); err != nil {
		return fmt.Errorf("create artifact directory: %w", err)
	}
	if err = os.Chmod(artifactDir, 0o700); err != nil {
		return fmt.Errorf("protect artifact directory: %w", err)
	}
	poster, err := layerProtocolPoster()
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(artifactDir, "input.png"), poster, 0o600); err != nil {
		return err
	}
	imageData := "data:image/png;base64," + base64.StdEncoding.EncodeToString(poster)
	adapter := provider.NewBytePlusWithSubmitTimeout(key, 5*time.Minute)
	key = ""
	downloadClient := safehttp.NewDownloadClient(90 * time.Second)
	cases := []layerProtocolCase{
		{Name: "auto-standard", Size: "auto", Mode: "standard"},
		{Name: "elements-fast", Prompt: "Separate the central figure, the CORNFIELD title, the lime circle, and the blue rectangle into independent layers.", Size: "1K", Mode: "fast"},
		{Name: "bbox-1.5k", Prompt: "Separate the title <bbox>80 50 600 160</bbox>, central figure <bbox>350 180 650 900</bbox>, lime circle <bbox>80 650 320 900</bbox>, and blue rectangle <bbox>700 600 930 880</bbox>.", Size: "1.5K", Mode: "standard"},
		{Name: "bbox-2k", Prompt: "Precisely separate the title <bbox>80 50 600 160</bbox>, central figure <bbox>350 180 650 900</bbox>, lime circle <bbox>80 650 320 900</bbox>, and blue rectangle <bbox>700 600 930 880</bbox>.", Size: "2K", Mode: "standard"},
	}
	report := layerProtocolReport{Model: "dola-seedream-5-0-pro-260628", Endpoint: "https://ark.ap-southeast.bytepluses.com/api/v3/images/generations", StartedAt: time.Now().UTC()}
	for _, item := range cases {
		result := layerProtocolResult{Name: item.Name, Size: item.Size, Mode: item.Mode, Status: "failed"}
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		response, callErr := adapter.DecomposeLayers(ctx, provider.LayerDecompositionRequest{
			Model: report.Model, Image: imageData, Prompt: item.Prompt, Size: item.Size, PromptOptimizationMode: item.Mode,
		})
		cancel()
		result.DurationMS = time.Since(started).Milliseconds()
		result.Usage = response.Usage
		result.ProviderRequestID = response.Telemetry.ProviderRequestID
		if callErr != nil {
			result.ErrorCode, result.ErrorMessage = providerErrorFields(callErr)
			report.Results = append(report.Results, result)
			report.CompletedAt = time.Now().UTC()
			_ = writeLayerProtocolReport(reportPath, report)
			return fmt.Errorf("layer protocol case %s failed: %s", item.Name, result.ErrorCode)
		}
		caseDir := filepath.Join(artifactDir, item.Name)
		if err = os.MkdirAll(caseDir, 0o700); err != nil {
			return err
		}
		if err = validateLayerProtocolResult(downloadClient, caseDir, response, &result); err != nil {
			result.ErrorCode, result.ErrorMessage = "CANARY_LAYER_INVALID", bounded(err.Error(), 1024)
			report.Results = append(report.Results, result)
			report.CompletedAt = time.Now().UTC()
			_ = writeLayerProtocolReport(reportPath, report)
			return fmt.Errorf("layer protocol case %s invalid: %w", item.Name, err)
		}
		result.Status = "passed"
		report.Results = append(report.Results, result)
		if err = writeLayerProtocolReport(reportPath, report); err != nil {
			return err
		}
	}
	report.CompletedAt = time.Now().UTC()
	return writeLayerProtocolReport(reportPath, report)
}

func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("secret file must contain one non-empty line")
	}
	return value, nil
}

func providerErrorFields(err error) (string, string) {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return providerErr.Code, bounded(providerErr.Message, 1024)
	}
	return "CANARY_PROVIDER_ERROR", bounded(err.Error(), 1024)
}

func validateLayerProtocolResult(client *http.Client, directory string, response provider.LayerDecompositionResult, result *layerProtocolResult) error {
	if len(response.Items) < 2 || len(response.Items) > 17 {
		return fmt.Errorf("unexpected output count %d", len(response.Items))
	}
	if generated, ok := numericUsage(response.Usage["generated_images"]); ok && generated != int64(len(response.Items)) {
		return fmt.Errorf("usage generated_images=%d, outputs=%d", generated, len(response.Items))
	}
	seen := make(map[int]struct{}, len(response.Items))
	files := make(map[int]string, len(response.Items))
	var base image.Image
	for index, item := range response.Items {
		if _, duplicate := seen[item.ZIndex]; duplicate {
			return fmt.Errorf("duplicate z_index %d", item.ZIndex)
		}
		seen[item.ZIndex] = struct{}{}
		if index == 0 && item.ZIndex != 0 {
			return errors.New("first output is not the base layer")
		}
		parsed, err := url.Parse(item.URL)
		if err != nil || parsed.Hostname() == "" {
			return errors.New("output URL is invalid")
		}
		result.OutputHosts = appendUnique(result.OutputHosts, strings.ToLower(parsed.Hostname()))
		if !layerOutputURLAllowed(parsed) {
			return errors.New("output URL is outside the BytePlus allowlist")
		}
		path := filepath.Join(directory, fmt.Sprintf("layer-%02d.png", item.ZIndex))
		if err = downloadProtocolLayer(client, item.URL, path); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		decoded, format, err := image.Decode(file)
		_ = file.Close()
		if err != nil || format != "png" {
			return fmt.Errorf("layer %d is not a decodable PNG", item.ZIndex)
		}
		if item.ZIndex > 0 && !imageHasTransparency(decoded) {
			return fmt.Errorf("layer %d has no transparent pixels", item.ZIndex)
		}
		if item.ZIndex == 0 {
			base = decoded
		}
		files[item.ZIndex] = path
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		hash, hashErr := fileSHA256(path)
		if hashErr != nil {
			return hashErr
		}
		result.OutputSHA256 = append(result.OutputSHA256, hash)
		result.OutputSizes = append(result.OutputSizes, fmt.Sprintf("%dx%d:%d", decoded.Bounds().Dx(), decoded.Bounds().Dy(), info.Size()))
	}
	if base == nil {
		return errors.New("base layer is missing")
	}
	baseBounds := base.Bounds()
	for _, item := range response.Items[1:] {
		if item.BoundingBox == nil || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("layer %d metadata is incomplete", item.ZIndex)
		}
		box := item.BoundingBox.Absolute
		if box[0] < 0 || box[1] < 0 || box[2] > baseBounds.Dx() || box[3] > baseBounds.Dy() {
			return fmt.Errorf("layer %d bounding box exceeds base", item.ZIndex)
		}
	}
	if err := recomposeProtocolLayers(response.Items, files, baseBounds, filepath.Join(directory, "recomposed.png")); err != nil {
		return err
	}
	result.OutputCount = len(response.Items)
	sort.Strings(result.OutputHosts)
	return nil
}

func downloadProtocolLayer(client *http.Client, rawURL, target string) error {
	current := rawURL
	for redirects := 0; redirects <= 3; redirects++ {
		parsed, err := url.Parse(current)
		if err != nil || !layerOutputURLAllowed(parsed) {
			return errors.New("layer URL is outside the BytePlus allowlist")
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, current, nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			location, parseErr := res.Location()
			res.Body.Close()
			if parseErr != nil || !layerOutputURLAllowed(location) {
				return errors.New("layer redirect rejected")
			}
			current = location.String()
			continue
		}
		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			return fmt.Errorf("layer download returned HTTP %d", res.StatusCode)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			res.Body.Close()
			return err
		}
		written, copyErr := io.Copy(file, io.LimitReader(res.Body, 50<<20+1))
		res.Body.Close()
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil || written > 50<<20 {
			_ = os.Remove(target)
			return errors.New("layer download failed or exceeded 50 MiB")
		}
		return nil
	}
	return errors.New("too many layer redirects")
}

func layerOutputURLAllowed(value *url.URL) bool {
	if value == nil || value.Scheme != "https" || value.User != nil || value.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
	return host == "bytepluses.com" || strings.HasSuffix(host, ".bytepluses.com") ||
		host == "byteplus.com" || strings.HasSuffix(host, ".byteplus.com") ||
		host == "tos-ap-southeast-1.volces.com" || strings.HasSuffix(host, ".tos-ap-southeast-1.volces.com")
}

func recomposeProtocolLayers(items []provider.LayerDecompositionItem, files map[int]string, bounds image.Rectangle, target string) error {
	canvas := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for _, item := range items {
		file, err := os.Open(files[item.ZIndex])
		if err != nil {
			return err
		}
		layer, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			return err
		}
		destination := canvas.Bounds()
		if item.BoundingBox != nil {
			box := item.BoundingBox.Absolute
			destination = image.Rect(box[0], box[1], box[2], box[3])
		}
		draw.BiLinear.Scale(canvas, destination, layer, layer.Bounds(), draw.Over, nil)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = png.Encode(file, canvas)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

func imageHasTransparency(value image.Image) bool {
	bounds := value.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := value.At(x, y).RGBA()
			if alpha < 0xffff {
				return true
			}
		}
	}
	return false
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func numericUsage(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func writeLayerProtocolReport(path string, report layerProtocolReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".layer-report-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func layerProtocolPoster() ([]byte, error) {
	const size = 2048
	canvas := image.NewRGBA(image.Rect(0, 0, size, size))
	stdDraw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.RGBA{R: 15, G: 17, B: 19, A: 255}), image.Point{}, stdDraw.Src)
	for y := 1300; y < 1780; y++ {
		for x := 160; x < 640; x++ {
			dx, dy := x-400, y-1540
			if dx*dx+dy*dy <= 240*240 {
				canvas.SetRGBA(x, y, color.RGBA{R: 209, G: 254, B: 23, A: 255})
			}
		}
	}
	stdDraw.Draw(canvas, image.Rect(1420, 1220, 1900, 1760), image.NewUniform(color.RGBA{R: 45, G: 88, B: 210, A: 255}), image.Point{}, stdDraw.Src)
	stdDraw.Draw(canvas, image.Rect(850, 720, 1198, 1700), image.NewUniform(color.RGBA{R: 226, G: 211, B: 191, A: 255}), image.Point{}, stdDraw.Src)
	for y := 420; y < 820; y++ {
		for x := 824; x < 1224; x++ {
			dx, dy := x-1024, y-620
			if dx*dx+dy*dy <= 200*200 {
				canvas.SetRGBA(x, y, color.RGBA{R: 226, G: 211, B: 191, A: 255})
			}
		}
	}
	textLayer := image.NewRGBA(image.Rect(0, 0, 160, 20))
	face := basicfont.Face7x13
	drawer := font.Drawer{Dst: textLayer, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(2, 14)}
	drawer.DrawString("C O R N F I E L D")
	draw.NearestNeighbor.Scale(canvas, image.Rect(160, 100, 1360, 300), textLayer, textLayer.Bounds(), draw.Over, nil)
	var output bytes.Buffer
	err := png.Encode(&output, canvas)
	return output.Bytes(), err
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
