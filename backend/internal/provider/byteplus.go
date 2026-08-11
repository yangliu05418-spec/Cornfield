package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	bytePlusBaseURL       = "https://ark.ap-southeast.bytepluses.com/api/v3"
	bytePlusProbeModel    = "dola-seedream-5-0-pro-260628"
	bytePlusMaxReferences = 10
	bytePlusMaxBodyBytes  = 160 << 20
	bytePlusProbeInterval = 5 * time.Minute
)

type BytePlus struct {
	APIKey  string
	BaseURL string
	Client  *http.Client

	probeMu       sync.Mutex
	probeAt       time.Time
	probeHealth   Health
	ProbeInterval time.Duration
}

func NewBytePlus(apiKey string) *BytePlus {
	return NewBytePlusWithSubmitTimeout(apiKey, 5*time.Minute)
}

func NewBytePlusWithSubmitTimeout(apiKey string, submitTimeout time.Duration) *BytePlus {
	if submitTimeout < time.Second {
		submitTimeout = 5 * time.Minute
	}
	return &BytePlus{
		APIKey: apiKey, BaseURL: bytePlusBaseURL,
		Client:        newHTTPClient(submitTimeout+20*time.Second, submitTimeout+10*time.Second),
		ProbeInterval: bytePlusProbeInterval,
	}
}

type bytePlusPromptOptions struct {
	Mode string `json:"mode"`
}

type bytePlusRequest struct {
	Model                 string                `json:"model"`
	Prompt                string                `json:"prompt"`
	Images                []string              `json:"image,omitempty"`
	Size                  string                `json:"size"`
	ResponseFormat        string                `json:"response_format"`
	OutputFormat          string                `json:"output_format"`
	Watermark             bool                  `json:"watermark"`
	OptimizePromptOptions bytePlusPromptOptions `json:"optimize_prompt_options"`
}

func (b *BytePlus) Submit(ctx context.Context, input CanonicalRequest) (Submission, error) {
	if input.ExpectedImages != 1 {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "Seedream 5.0 Pro produces one image per request"}
	}
	if strings.TrimSpace(input.Size) == "" {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "Seedream 5.0 Pro requires an explicit output size"}
	}
	if utf8.RuneCountInString(input.Prompt) > 8192 {
		return Submission{}, &Error{Code: "PROMPT_TOO_LONG", Message: "final BytePlus prompt exceeds 8192 characters"}
	}
	references := append([]string(nil), input.ReferenceData...)
	references = append(references, input.ReferenceURLs...)
	if len(references) > bytePlusMaxReferences {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "Seedream 5.0 Pro supports at most 10 reference images"}
	}
	mode := "standard"
	if input.Options.Image != nil && input.Options.Image.PromptOptimizationMode != "" {
		mode = input.Options.Image.PromptOptimizationMode
	}
	if mode != "standard" && mode != "fast" {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "unsupported BytePlus prompt optimization mode"}
	}
	payload := bytePlusRequest{
		Model: input.Model, Prompt: input.Prompt, Images: references, Size: input.Size,
		ResponseFormat: "b64_json", OutputFormat: "png", Watermark: false,
		OptimizePromptOptions: bytePlusPromptOptions{Mode: mode},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	req.Header.Set("Content-Type", "application/json")
	var requestWritten atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) { requestWritten.Store(true) },
	}))
	started := time.Now()
	res, err := b.Client.Do(req)
	if err != nil {
		if !requestWritten.Load() {
			return Submission{}, &Error{Code: "PROVIDER_CONNECT_FAILED", Message: "provider connection failed before the request was written", Retryable: true}
		}
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: sanitizeProviderErrorDetail(err.Error(), []string{b.APIKey, input.Prompt}), SubmissionUncertain: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Submission{}, bytePlusHTTPError(res, b.APIKey, input.Prompt)
	}
	response, err := decodeBytePlusResponse(res.Body)
	telemetry := responseTelemetryExcluding(res, []string{b.APIKey, input.Prompt})
	if err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BytePlus accepted the request but returned an invalid response", SubmissionUncertain: true, Telemetry: telemetry}
	}
	if response.Error.Code != "" || response.Error.Message != "" {
		return Submission{}, bytePlusStructuredError(res.StatusCode, response.Error, telemetry, b.APIKey, input.Prompt)
	}
	images := make([]Image, 0, len(response.Data))
	for _, item := range response.Data {
		if item.Error.Code != "" || item.Error.Message != "" {
			return Submission{}, bytePlusStructuredError(res.StatusCode, item.Error, telemetry, b.APIKey, input.Prompt)
		}
		if len(item.B64) == 0 {
			return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BytePlus returned an empty image after accepting the request", SubmissionUncertain: true, Telemetry: telemetry}
		}
		images = append(images, Image{Bytes: item.B64, MediaType: "image/png"})
	}
	if len(images) != 1 {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BytePlus returned an unexpected image count after accepting the request", SubmissionUncertain: true, Telemetry: telemetry}
	}
	usage := response.Usage
	if usage == nil {
		usage = make(map[string]any)
	}
	usage["duration_ms"] = time.Since(started).Milliseconds()
	return Submission{
		ProviderJobID: telemetry.ProviderRequestID,
		Completed:     true,
		Result:        Result{Status: "completed", Images: images, Usage: usage, Telemetry: telemetry},
		Telemetry:     telemetry,
	}, nil
}

type bytePlusErrorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
	Type    string `json:"type"`
}

type bytePlusResponse struct {
	Data []struct {
		B64          decodedBase64         `json:"b64_json"`
		OutputFormat string                `json:"output_format"`
		Size         string                `json:"size"`
		Error        bytePlusErrorEnvelope `json:"error"`
	} `json:"data"`
	Usage map[string]any        `json:"usage"`
	Error bytePlusErrorEnvelope `json:"error"`
}

func decodeBytePlusResponse(body io.Reader) (bytePlusResponse, error) {
	var response bytePlusResponse
	limited := &io.LimitedReader{R: body, N: bytePlusMaxBodyBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&response); err != nil {
		return response, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return response, fmt.Errorf("response contains trailing JSON")
		}
		return response, err
	}
	if limited.N == 0 {
		return response, fmt.Errorf("response exceeds %d bytes", bytePlusMaxBodyBytes)
	}
	return response, nil
}

func bytePlusHTTPError(res *http.Response, secrets ...string) error {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var envelope struct {
		Error bytePlusErrorEnvelope `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	telemetry := responseTelemetryExcluding(res, secrets)
	providerErr := bytePlusStructuredError(res.StatusCode, envelope.Error, telemetry, secrets...)
	if typed, ok := providerErr.(*Error); ok {
		typed.RetryAfter = parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	}
	return providerErr
}

func bytePlusStructuredError(status int, upstream bytePlusErrorEnvelope, telemetry Telemetry, secrets ...string) error {
	detail := strings.TrimSpace(strings.Join([]string{upstream.Code, upstream.Message}, ": "))
	detail = sanitizeProviderErrorDetail(detail, secrets)
	message := fmt.Sprintf("provider returned HTTP %d", status)
	if detail != "" {
		message += ": " + detail
	}
	combined := strings.ToLower(upstream.Code + " " + upstream.Type + " " + upstream.Message)
	if contentPolicyErrorDetail(combined) || strings.Contains(combined, "contentfilter") || strings.Contains(combined, "moderation") {
		return &Error{Code: "CONTENT_POLICY_REJECTED", Message: message, Telemetry: telemetry}
	}
	if status == http.StatusRequestTimeout || status >= 500 {
		return &Error{Code: "SUBMISSION_UNCERTAIN", Message: message, SubmissionUncertain: true, Telemetry: telemetry}
	}
	if status >= 200 && status < 300 {
		return &Error{Code: "PROVIDER_RESPONSE_INVALID", Message: message, Telemetry: telemetry}
	}
	code := fmt.Sprintf("PROVIDER_HTTP_%d", status)
	if status == 0 {
		code = "PROVIDER_RESPONSE_INVALID"
	}
	return &Error{
		Code:          code,
		Message:       message,
		Retryable:     status == http.StatusTooManyRequests,
		PauseProvider: status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired,
		Telemetry:     telemetry,
	}
}

func (b *BytePlus) Poll(context.Context, Submission) (Result, error) {
	return Result{}, &Error{Code: "POLL_UNSUPPORTED", Message: "BytePlus image calls complete synchronously"}
}

func (b *BytePlus) Cancel(context.Context, Submission) (CancelResult, error) {
	return CancelResult{Accepted: false, Mode: "discard_result_only"}, nil
}

func (b *BytePlus) Probe(ctx context.Context) Health {
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	interval := b.ProbeInterval
	if interval <= 0 {
		interval = bytePlusProbeInterval
	}
	if !b.probeAt.IsZero() && time.Since(b.probeAt) < interval {
		return b.probeHealth
	}
	health := b.probe(ctx)
	b.probeAt, b.probeHealth = time.Now(), health
	return health
}

func (b *BytePlus) probe(ctx context.Context) Health {
	body, _ := json.Marshal(map[string]string{"model": bytePlusProbeModel})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/images/generations", bytes.NewReader(body))
	if err != nil {
		return Health{Message: "probe request invalid"}
	}
	req.Header.Set("Authorization", "Bearer "+b.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := b.Client.Do(req)
	if err != nil {
		return Health{Message: sanitizeProviderErrorDetail(err.Error(), []string{b.APIKey})}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var envelope struct {
		Error bytePlusErrorEnvelope `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	missingPrompt := res.StatusCode == http.StatusBadRequest &&
		strings.HasPrefix(strings.ToLower(envelope.Error.Code), "missingparameter") &&
		(strings.EqualFold(envelope.Error.Param, "prompt") || strings.Contains(strings.ToLower(envelope.Error.Message), "prompt"))
	if missingPrompt {
		return Health{Healthy: true, Message: "authenticated"}
	}
	code := sanitizeProviderErrorDetail(envelope.Error.Code, []string{b.APIKey})
	return Health{Message: strings.TrimSpace(fmt.Sprintf("HTTP %d %s", res.StatusCode, code))}
}
