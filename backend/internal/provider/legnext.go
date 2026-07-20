package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Legnext struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func NewLegnext(apiKey string) *Legnext {
	return &Legnext{APIKey: apiKey, BaseURL: "https://api.legnext.ai", Client: newHTTPClient(45*time.Second, 30*time.Second)}
}

type legnextTask struct {
	JobID  string `json:"job_id"`
	TaskID string `json:"task_id"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Data   *struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
	} `json:"data,omitempty"`
	Output struct {
		ImageURL  string   `json:"image_url"`
		ImageURLs []string `json:"image_urls"`
	} `json:"output"`
	Meta struct {
		Usage map[string]any `json:"usage"`
	} `json:"meta"`
}

func (task legnextTask) providerJobID() string {
	for _, candidate := range []string{task.JobID, task.TaskID, task.ID} {
		if value := strings.TrimSpace(candidate); value != "" {
			return value
		}
	}
	if task.Data != nil {
		for _, candidate := range []string{task.Data.JobID, task.Data.TaskID, task.Data.ID} {
			if value := strings.TrimSpace(candidate); value != "" {
				return value
			}
		}
	}
	return ""
}

func (l *Legnext) Submit(ctx context.Context, input CanonicalRequest) (Submission, error) {
	parts := make([]string, 0, len(input.ReferenceURLs)+16)
	for _, referenceURL := range input.ReferenceURLs {
		parsed, err := url.Parse(referenceURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
			return Submission{}, &Error{Code: "REFERENCE_URL_INVALID", Message: "Legnext reference images require absolute HTTPS URLs"}
		}
		parts = append(parts, referenceURL)
	}
	parts = append(parts, strings.TrimSpace(input.Prompt))
	if input.AspectRatio != "" {
		parts = append(parts, "--ar", input.AspectRatio)
	}
	if options := input.Options.Midjourney; options != nil {
		if options.Quality != nil {
			parts = append(parts, "--q", strconv.Itoa(*options.Quality))
		}
		parts = append(parts, "--stylize", strconv.Itoa(options.Stylize), "--chaos", strconv.Itoa(options.Chaos), "--weird", strconv.Itoa(options.Weird))
		if options.ImageWeight != nil {
			parts = append(parts, "--iw", strconv.FormatFloat(*options.ImageWeight, 'f', -1, 64))
		}
		parts = append(parts, "--"+options.Speed, "--v", options.Version)
		if options.Resolution == "hd" {
			parts = append(parts, "--hd")
		}
		if options.Raw {
			parts = append(parts, "--raw")
		}
		if options.Tile {
			parts = append(parts, "--tile")
		}
		if options.Draft {
			parts = append(parts, "--draft")
		}
	}
	providerPrompt := strings.Join(parts, " ")
	if utf8.RuneCountInString(providerPrompt) > 8192 {
		return Submission{}, &Error{Code: "PROMPT_TOO_LONG", Message: "final Legnext prompt exceeds 8192 characters"}
	}
	payload, err := json.Marshal(struct {
		Text     string `json:"text"`
		Callback string `json:"callback,omitempty"`
	}{Text: providerPrompt, Callback: input.CallbackURL})
	if err != nil {
		return Submission{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.BaseURL, "/")+"/api/v1/diffusion", bytes.NewReader(payload))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("x-api-key", l.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := l.Client.Do(req)
	if err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: err.Error(), SubmissionUncertain: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Submission{}, legnextHTTPError(res, true, l.APIKey)
	}
	var task legnextTask
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&task) != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "Legnext accepted the request but returned invalid JSON", SubmissionUncertain: true, Telemetry: responseTelemetryExcluding(res, []string{l.APIKey})}
	}
	providerJobID := normalizeProviderIdentifier(task.providerJobID(), l.APIKey)
	telemetry := responseTelemetryExcluding(res, []string{l.APIKey}, providerJobID)
	if providerJobID == "" {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "Legnext accepted the request without returning a task identifier", SubmissionUncertain: true, Telemetry: telemetry}
	}
	return Submission{ProviderJobID: providerJobID, Telemetry: telemetry}, nil
}

func (l *Legnext) Poll(ctx context.Context, submission Submission) (Result, error) {
	endpoint := strings.TrimRight(l.BaseURL, "/") + "/api/v1/job/" + url.PathEscape(submission.ProviderJobID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("x-api-key", l.APIKey)
	res, err := l.Client.Do(req)
	if err != nil {
		return Result{}, &Error{Code: "PROVIDER_POLL_FAILED", Message: err.Error(), Retryable: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, legnextHTTPError(res, false, l.APIKey)
	}
	var task legnextTask
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&task) != nil {
		return Result{}, &Error{Code: "PROVIDER_RESPONSE_INVALID", Message: "invalid Legnext task response", Retryable: true, Telemetry: responseTelemetryExcluding(res, []string{l.APIKey})}
	}
	result := Result{Status: task.Status, Usage: task.Meta.Usage, Telemetry: responseTelemetryExcluding(res, []string{l.APIKey}, task.providerJobID())}
	if task.Status == "completed" {
		urls := task.Output.ImageURLs
		if len(urls) == 0 && task.Output.ImageURL != "" {
			urls = []string{task.Output.ImageURL}
		}
		for _, item := range urls {
			result.Images = append(result.Images, Image{URL: item})
		}
	}
	if task.Status == "failed" {
		result.ErrorCode = "LEGNEXT_JOB_FAILED"
		result.ErrorText = "Legnext generation failed"
	}
	return result, nil
}

func (l *Legnext) Cancel(context.Context, Submission) (CancelResult, error) {
	return CancelResult{Accepted: false, Mode: "discard_result_only"}, nil
}

func legnextHTTPError(res *http.Response, duringSubmit bool, secrets ...string) error {
	if res.StatusCode == http.StatusForbidden {
		var envelope struct {
			Message    string `json:"message"`
			RawMessage string `json:"raw_message"`
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		_ = json.Unmarshal(body, &envelope)
		message := strings.ToLower(strings.TrimSpace(envelope.Message + " " + envelope.RawMessage))
		sensitive := strings.Contains(message, "sensitive content") || strings.Contains(message, "content policy")
		if sensitive && !strings.Contains(message, "permission") {
			return &Error{
				Code:      "CONTENT_POLICY_REJECTED",
				Message:   "Legnext rejected the request under its content policy",
				Telemetry: responseTelemetryExcluding(res, secrets),
			}
		}
		// Legnext also uses 403 for permission failures. Treat an unknown 403 as
		// provider-wide until an authenticated operator confirms otherwise; a
		// false content-policy classification would keep draining the queue.
		return &Error{
			Code:          "PROVIDER_HTTP_403",
			Message:       "provider returned HTTP 403",
			PauseProvider: true,
			Telemetry:     responseTelemetryExcluding(res, secrets),
		}
	}
	if duringSubmit {
		return httpSubmissionError(res, secrets...)
	}
	return httpProviderError(res, secrets...)
}

func (l *Legnext) Probe(ctx context.Context) Health {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(l.BaseURL, "/")+"/api/account/balance", nil)
	req.Header.Set("x-api-key", l.APIKey)
	res, err := l.Client.Do(req)
	if err != nil {
		return Health{Message: err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Health{Message: res.Status}
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			BalanceUSD       float64 `json:"balance_usd"`
			AvailableCredits int64   `json:"available_credits"`
			AvailablePoints  int64   `json:"available_points"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); err != nil || envelope.Code != http.StatusOK {
		return Health{Message: "invalid balance response"}
	}
	if envelope.Data.BalanceUSD <= 0 && envelope.Data.AvailableCredits <= 0 && envelope.Data.AvailablePoints <= 0 {
		return Health{Message: "balance exhausted"}
	}
	return Health{Healthy: true, Message: res.Status}
}
