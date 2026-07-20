package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type BFL struct {
	APIKey  string
	BaseURL string
	Client  *http.Client
}

func NewBFL(apiKey string) *BFL {
	return &BFL{APIKey: apiKey, BaseURL: "https://api.bfl.ai", Client: newHTTPClient(45*time.Second, 30*time.Second)}
}

func (b *BFL) Submit(ctx context.Context, input CanonicalRequest) (Submission, error) {
	width, height, err := bflDimensions(input.AspectRatio, input.Resolution)
	if err != nil {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: err.Error()}
	}
	if len(input.ReferenceURLs) > 8 || len(input.ReferenceData) > 0 {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "FLUX.2 Max accepts at most eight URL references"}
	}
	payload := map[string]any{
		"prompt": strings.TrimSpace(input.Prompt), "width": width, "height": height,
		"output_format": "png", "safety_tolerance": 2,
	}
	for index, reference := range input.ReferenceURLs {
		field := "input_image"
		if index > 0 {
			field += "_" + strconv.Itoa(index+1)
		}
		payload[field] = reference
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.BaseURL, "/")+"/v1/"+url.PathEscape(input.Model), bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("x-key", b.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := b.Client.Do(req)
	if err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BFL submission transport failed", SubmissionUncertain: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Submission{}, httpSubmissionError(res, b.APIKey)
	}
	var response struct {
		ID         string  `json:"id"`
		PollingURL string  `json:"polling_url"`
		Cost       float64 `json:"cost"`
		InputMP    float64 `json:"input_mp"`
		OutputMP   float64 `json:"output_mp"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&response); err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BFL accepted the request but returned invalid JSON", SubmissionUncertain: true, Telemetry: responseTelemetryExcluding(res, []string{b.APIKey})}
	}
	response.ID = normalizeProviderIdentifier(response.ID, b.APIKey)
	if response.ID == "" || !validBFLPollingURL(response.PollingURL) {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "BFL accepted the request without a valid task identifier and polling URL", SubmissionUncertain: true, Telemetry: responseTelemetryExcluding(res, []string{b.APIKey}, response.ID)}
	}
	return Submission{ProviderJobID: response.ID, PollingURL: response.PollingURL, Telemetry: responseTelemetryExcluding(res, []string{b.APIKey}, response.ID)}, nil
}

func (b *BFL) Poll(ctx context.Context, submission Submission) (Result, error) {
	pollingURL := submission.PollingURL
	if !validBFLPollingURL(pollingURL) {
		return Result{}, &Error{Code: "PROVIDER_POLL_URL_INVALID", Message: "BFL polling requires the URL returned by submission"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, pollingURL, nil)
	req.Header.Set("x-key", b.APIKey)
	req.Header.Set("Accept", "application/json")
	res, err := b.Client.Do(req)
	if err != nil {
		return Result{}, &Error{Code: "PROVIDER_POLL_FAILED", Message: "BFL polling transport failed", Retryable: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Result{}, httpProviderError(res, b.APIKey)
	}
	var response struct {
		Status string `json:"status"`
		Result struct {
			Sample string `json:"sample"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return Result{}, &Error{Code: "PROVIDER_RESPONSE_INVALID", Message: "invalid BFL task response", Retryable: true, Telemetry: responseTelemetryExcluding(res, []string{b.APIKey})}
	}
	result := Result{Status: "pending", Telemetry: responseTelemetryExcluding(res, []string{b.APIKey}, submission.ProviderJobID)}
	switch strings.ToLower(strings.TrimSpace(response.Status)) {
	case "ready":
		if strings.TrimSpace(response.Result.Sample) == "" {
			return Result{}, &Error{Code: "PROVIDER_RESPONSE_INVALID", Message: "BFL task completed without an image URL", Retryable: true, Telemetry: result.Telemetry}
		}
		result.Status = "completed"
		result.Images = []Image{{URL: response.Result.Sample}}
	case "pending":
	case "request moderated", "content moderated":
		result.Status = "failed"
		result.ErrorCode = "CONTENT_POLICY_REJECTED"
		result.ErrorText = "BFL rejected the request under its content policy"
	case "error", "failed", "task not found":
		result.Status = "failed"
		result.ErrorCode = "BFL_JOB_FAILED"
		result.ErrorText = "BFL generation failed"
	default:
		return Result{}, &Error{Code: "PROVIDER_RESPONSE_INVALID", Message: "BFL returned an unknown task status", Retryable: true, Telemetry: result.Telemetry}
	}
	return result, nil
}

func (b *BFL) Cancel(context.Context, Submission) (CancelResult, error) {
	return CancelResult{Accepted: false, Mode: "discard_result_only"}, nil
}

func (b *BFL) Probe(ctx context.Context) Health {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(b.BaseURL, "/")+"/v1/credits", nil)
	req.Header.Set("x-key", b.APIKey)
	res, err := b.Client.Do(req)
	if err != nil {
		return Health{Message: "BFL probe failed"}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Health{Message: res.Status}
	}
	return Health{Healthy: true, Message: res.Status}
}

func validBFLPollingURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.bfl.ai", "api.eu.bfl.ai", "api.us.bfl.ai":
		return true
	default:
		return false
	}
}

func bflDimensions(ratio, resolution string) (int, int, error) {
	parts := strings.Split(ratio, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid BFL aspect ratio")
	}
	ratioWidth, widthErr := strconv.ParseFloat(parts[0], 64)
	ratioHeight, heightErr := strconv.ParseFloat(parts[1], 64)
	if widthErr != nil || heightErr != nil || ratioWidth <= 0 || ratioHeight <= 0 {
		return 0, 0, fmt.Errorf("invalid BFL aspect ratio")
	}
	targets := map[string]float64{"1MP": 1_000_000, "2MP": 2_000_000, "4MP": 4_000_000}
	target, ok := targets[resolution]
	if !ok {
		return 0, 0, fmt.Errorf("invalid BFL resolution tier")
	}
	width := int(math.Floor(math.Sqrt(target*ratioWidth/ratioHeight)/16)) * 16
	height := int(math.Floor(math.Sqrt(target*ratioHeight/ratioWidth)/16)) * 16
	if width < 64 || height < 64 || width*height > 4_000_000 {
		return 0, 0, fmt.Errorf("BFL dimensions are outside the supported range")
	}
	return width, height, nil
}
