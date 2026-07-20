package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type OpenRouter struct {
	APIKey    string
	PublicURL string
	BaseURL   string
	Client    *http.Client
}

func NewOpenRouter(apiKey, publicURL string) *OpenRouter {
	return &OpenRouter{APIKey: apiKey, PublicURL: publicURL, BaseURL: "https://openrouter.ai", Client: newHTTPClient(5*time.Minute, 4*time.Minute)}
}

func (o *OpenRouter) Submit(ctx context.Context, input CanonicalRequest) (Submission, error) {
	parameters := make(map[string]struct{}, len(input.RequestParameters))
	for _, parameter := range input.RequestParameters {
		parameters[parameter] = struct{}{}
	}
	supports := func(parameter string) bool {
		_, ok := parameters[parameter]
		return ok
	}
	referenceCount := len(input.ReferenceData) + len(input.ReferenceURLs)
	if referenceCount > 0 && !supports("input_references") {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "model does not support input_references"}
	}
	if input.ExpectedImages > 1 && !supports("n") {
		return Submission{}, &Error{Code: "UNSUPPORTED_PARAMETER", Message: "model does not support multiple images"}
	}

	references := make([]map[string]any, 0, referenceCount)
	for _, data := range input.ReferenceData {
		references = append(references, map[string]any{"type": "image_url", "image_url": map[string]string{"url": data}})
	}
	for _, referenceURL := range input.ReferenceURLs {
		references = append(references, map[string]any{"type": "image_url", "image_url": map[string]string{"url": referenceURL}})
	}
	prompt := strings.TrimSpace(input.Prompt)
	if input.PromptAspectRatio && input.AspectRatio != "" && input.AspectRatio != "auto" {
		prompt += "\n\nMandatory composition requirement: frame the final image in a " + input.AspectRatio + " aspect ratio. Compose every subject and background for that exact canvas orientation; do not add borders, letterboxing, or a collage."
	}
	if utf8.RuneCountInString(prompt) > 8192 {
		return Submission{}, &Error{Code: "PROMPT_TOO_LONG", Message: "final OpenRouter prompt exceeds 8192 characters"}
	}
	payload := map[string]any{"model": input.Model, "prompt": prompt}
	if supports("n") && input.ExpectedImages > 0 {
		payload["n"] = input.ExpectedImages
	}
	if supports("aspect_ratio") && input.AspectRatio != "" {
		payload["aspect_ratio"] = input.AspectRatio
	}
	if supports("resolution") && input.Resolution != "" {
		payload["resolution"] = input.Resolution
	}
	if supports("output_format") {
		payload["output_format"] = "png"
	}
	if supports("quality") && input.Options.Image != nil && input.Options.Image.Quality != "" {
		payload["quality"] = input.Options.Image.Quality
	}
	if len(references) > 0 {
		payload["input_references"] = references
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Submission{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/api/v1/images", bytes.NewReader(body))
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", o.PublicURL)
	req.Header.Set("X-Title", "Cornfield")
	started := time.Now()
	res, err := o.Client.Do(req)
	if err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: err.Error(), SubmissionUncertain: true}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Submission{}, openRouterSubmissionError(res, o.APIKey)
	}
	responseID, images, usage, err := decodeOpenRouterResponse(res.Body)
	if err != nil {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "OpenRouter accepted the request but returned an invalid response", SubmissionUncertain: true, Telemetry: responseTelemetryExcluding(res, []string{o.APIKey})}
	}
	responseID = normalizeProviderIdentifier(responseID, o.APIKey)
	telemetry := responseTelemetryExcluding(res, []string{o.APIKey}, responseID)
	if responseID == "" {
		responseID = normalizeProviderIdentifier(res.Header.Get("X-Generation-Id"), o.APIKey)
	}
	result := Result{Status: "completed", Usage: usage, Images: images, Telemetry: telemetry}
	if result.Usage == nil {
		result.Usage = make(map[string]any)
	}
	if len(result.Images) == 0 {
		return Submission{}, &Error{Code: "SUBMISSION_UNCERTAIN", Message: "provider returned no images after accepting the request", SubmissionUncertain: true, Telemetry: telemetry}
	}
	result.Usage["duration_ms"] = time.Since(started).Milliseconds()
	return Submission{ProviderJobID: responseID, Completed: true, Result: result, Telemetry: telemetry}, nil
}

const maxOpenRouterImageBytes = 50 << 20

type decodedBase64 []byte

func (decoded *decodedBase64) UnmarshalJSON(raw []byte) error {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return fmt.Errorf("base64 image must be a JSON string")
	}
	encoded := raw[1 : len(raw)-1]
	if bytes.IndexByte(encoded, '\\') >= 0 {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		encoded = []byte(value)
	}
	decodedLen := base64.StdEncoding.DecodedLen(len(encoded))
	if rawLen := base64.RawStdEncoding.DecodedLen(len(encoded)); rawLen > decodedLen {
		decodedLen = rawLen
	}
	if decodedLen > maxOpenRouterImageBytes {
		return fmt.Errorf("decoded image exceeds %d bytes", maxOpenRouterImageBytes)
	}
	data := make([]byte, decodedLen)
	n, err := base64.StdEncoding.Decode(data, encoded)
	if err != nil {
		n, err = base64.RawStdEncoding.Decode(data, encoded)
	}
	if err != nil || n == 0 {
		return fmt.Errorf("invalid base64 image")
	}
	*decoded = data[:n]
	return nil
}

func decodeOpenRouterResponse(body io.Reader) (string, []Image, map[string]any, error) {
	decoder := json.NewDecoder(io.LimitReader(body, 160<<20))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return "", nil, nil, fmt.Errorf("response is not a JSON object")
	}
	var responseID string
	var images []Image
	var usage map[string]any
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", nil, nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", nil, nil, fmt.Errorf("invalid response field")
		}
		switch key {
		case "id":
			if err := decoder.Decode(&responseID); err != nil {
				return "", nil, nil, err
			}
		case "data":
			start, err := decoder.Token()
			if err != nil || start != json.Delim('[') {
				return "", nil, nil, fmt.Errorf("data is not an array")
			}
			for decoder.More() {
				var item struct {
					B64       decodedBase64 `json:"b64_json"`
					MediaType string        `json:"media_type"`
				}
				if err := decoder.Decode(&item); err != nil {
					return "", nil, nil, err
				}
				if len(item.B64) == 0 {
					return "", nil, nil, fmt.Errorf("image has no b64_json")
				}
				if item.MediaType == "" {
					item.MediaType = "image/png"
				}
				images = append(images, Image{Bytes: item.B64, MediaType: item.MediaType})
			}
			if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
				return "", nil, nil, fmt.Errorf("data array is not closed")
			}
		case "usage":
			if err := decoder.Decode(&usage); err != nil {
				return "", nil, nil, err
			}
		default:
			var discard json.RawMessage
			if err := decoder.Decode(&discard); err != nil {
				return "", nil, nil, err
			}
		}
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return "", nil, nil, fmt.Errorf("response object is not closed")
	}
	return responseID, images, usage, nil
}

func (o *OpenRouter) Poll(context.Context, Submission) (Result, error) {
	return Result{}, &Error{Code: "POLL_UNSUPPORTED", Message: "OpenRouter image calls complete synchronously"}
}

func (o *OpenRouter) Cancel(context.Context, Submission) (CancelResult, error) {
	return CancelResult{Accepted: false, Mode: "discard_result_only"}, nil
}

func (o *OpenRouter) Probe(ctx context.Context) Health {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(o.BaseURL, "/")+"/api/v1/key", nil)
	req.Header.Set("Authorization", "Bearer "+o.APIKey)
	res, err := o.Client.Do(req)
	if err != nil {
		return Health{Message: err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return Health{Message: res.Status}
	}
	var envelope struct {
		Data struct {
			Limit          *float64 `json:"limit"`
			Usage          float64  `json:"usage"`
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope); err != nil {
		return Health{Message: "invalid key status response"}
	}
	if (envelope.Data.LimitRemaining != nil && *envelope.Data.LimitRemaining <= 0) || (envelope.Data.Limit != nil && envelope.Data.Usage >= *envelope.Data.Limit) {
		return Health{Message: "quota exhausted"}
	}
	return Health{Healthy: true, Message: res.Status}
}

func httpProviderError(res *http.Response, secrets ...string) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	message := fmt.Sprintf("provider returned HTTP %d", res.StatusCode)
	retryable := res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
	// A 403 can be a per-request content-policy rejection or model-level
	// authorization failure; neither proves the whole credential is unusable.
	// Only definitive key/quota failures pause every queued request.
	pause := res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusPaymentRequired
	providerError := &Error{Code: fmt.Sprintf("PROVIDER_HTTP_%d", res.StatusCode), Message: message, Retryable: retryable, PauseProvider: pause, Telemetry: responseTelemetryExcluding(res, secrets)}
	providerError.RetryAfter = parseRetryAfter(res.Header.Get("Retry-After"), time.Now())
	return providerError
}

func httpSubmissionError(res *http.Response, secrets ...string) error {
	providerError := httpProviderError(res, secrets...).(*Error)
	if res.StatusCode >= 500 {
		providerError.Retryable = false
		providerError.SubmissionUncertain = true
		providerError.Code = "SUBMISSION_UNCERTAIN"
	}
	return providerError
}

func openRouterSubmissionError(res *http.Response, secrets ...string) error {
	// The Dedicated Image API documents 502 as an unfinished/cancelled
	// generation that is not billed. A received 502 is therefore a definite,
	// safely retryable rejection. Other 5xx responses remain ambiguous because
	// the provider gives no equivalent no-charge guarantee for them.
	if res.StatusCode == http.StatusBadGateway {
		return httpProviderError(res, secrets...)
	}
	return httpSubmissionError(res, secrets...)
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}
