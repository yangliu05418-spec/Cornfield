package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenRouterSubmitCapabilityGatesPayload(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/images" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Generation-Id", "generation-1")
		w.Header().Set("X-Request-Id", "request-1")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"cG5n","media_type":"image/png"}]}`))
	}))
	defer server.Close()

	adapter := NewOpenRouter("test-key", "https://cornfield.test")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	result, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model:             "openai/gpt-image-1",
		Prompt:            "a corn field",
		AspectRatio:       "16:9",
		Resolution:        "2K",
		ExpectedImages:    1,
		ReferenceURLs:     []string{"https://cornfield.test/provider-input/opaque-token"},
		RequestParameters: []string{"n", "input_references"},
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !result.Completed || result.ProviderJobID != "generation-1" || len(result.Result.Images) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Telemetry.ProviderRequestID != "request-1" || result.Telemetry.HTTPStatus != http.StatusOK {
		t.Fatalf("telemetry = %+v", result.Telemetry)
	}
	for _, field := range []string{"aspect_ratio", "resolution", "output_format"} {
		if _, exists := payload[field]; exists {
			t.Errorf("unsupported field %q was sent", field)
		}
	}
	if payload["n"] != float64(1) {
		t.Errorf("n = %#v", payload["n"])
	}
	if references, ok := payload["input_references"].([]any); !ok || len(references) != 1 {
		t.Errorf("input_references = %#v", payload["input_references"])
	}
}

func TestOpenRouterConnectFailureBeforeWriteIsSafelyRetryable(t *testing.T) {
	adapter := NewOpenRouter("test-key", "")
	adapter.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: "model", Prompt: "prompt", ExpectedImages: 1})
	var providerErr *Error
	if !errors.As(err, &providerErr) || !providerErr.Retryable || providerErr.SubmissionUncertain || providerErr.Code != "PROVIDER_CONNECT_FAILED" {
		t.Fatalf("error = %#v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestOpenRouterQualityAndPromptAspectRatio(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"id":"generation-1","data":[{"b64_json":"cG5n","media_type":"image/png"}]}`))
	}))
	defer server.Close()
	adapter := NewOpenRouter("test-key", "")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model: "openai/gpt-image-2", Prompt: "a corn field", AspectRatio: "16:9", PromptAspectRatio: true,
		ExpectedImages: 1, RequestParameters: []string{"quality", "n"}, Options: GenerationOptions{Image: &ImageOptions{Quality: "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["quality"] != "high" || payload["aspect_ratio"] != nil || payload["resolution"] != nil {
		t.Fatalf("payload = %#v", payload)
	}
	prompt, _ := payload["prompt"].(string)
	if !strings.Contains(prompt, "16:9 aspect ratio") || !strings.HasPrefix(prompt, "a corn field") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestOpenRouterExplicitSizeSuppressesResolutionAndAspectRatio(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"cG5n","media_type":"image/png"}]}`))
	}))
	defer server.Close()
	adapter := NewOpenRouter("test-key", "")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model: "bytedance-seed/seedream-4.5", Prompt: "a corn field", AspectRatio: "16:9", Resolution: "2K", Size: "2560x1440",
		ExpectedImages: 1, RequestParameters: []string{"size", "resolution", "aspect_ratio", "n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["size"] != "2560x1440" || payload["resolution"] != nil || payload["aspect_ratio"] != nil {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestOpenRouterSubmitRejectsUngatedReferenceImages(t *testing.T) {
	adapter := NewOpenRouter("test-key", "")
	_, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model:         "model",
		Prompt:        "prompt",
		ReferenceData: []string{"data:image/png;base64,cG5n"},
	})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Code != "UNSUPPORTED_PARAMETER" {
		t.Fatalf("error = %#v", err)
	}
}

func TestOpenRouterSubmissionHTTPClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		retryable bool
		uncertain bool
	}{
		{name: "rate limited is safe to retry", status: http.StatusTooManyRequests, retryable: true},
		{name: "documented unfinished generation is safe to retry", status: http.StatusBadGateway, retryable: true},
		{name: "other server error is ambiguous", status: http.StatusInternalServerError, uncertain: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.Header().Set("Request-Id", "failure-request-1")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream unavailable"}}`))
			}))
			defer server.Close()
			adapter := NewOpenRouter("test-key", "")
			adapter.BaseURL = server.URL
			adapter.Client = server.Client()
			_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: "model", Prompt: "prompt", ExpectedImages: 1})
			var providerErr *Error
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %#v", err)
			}
			if providerErr.Message != fmt.Sprintf("provider returned HTTP %d: upstream unavailable", test.status) || providerErr.Retryable != test.retryable || providerErr.SubmissionUncertain != test.uncertain {
				t.Errorf("provider error = %+v", providerErr)
			}
			if providerErr.RetryAfter != 7*time.Second {
				t.Errorf("RetryAfter = %v", providerErr.RetryAfter)
			}
			if providerErr.Telemetry.ProviderRequestID != "failure-request-1" || providerErr.Telemetry.HTTPStatus != test.status {
				t.Errorf("telemetry = %+v", providerErr.Telemetry)
			}
		})
	}
}

func TestOpenRouterErrorDetailIsBoundedAndRedacted(t *testing.T) {
	secret := "sk-secret-value-123456789"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"message":"invalid image at https://private.example/file?token=%s using %s and data:image/png;base64,QUJDREVGRw==","metadata":{"provider_name":"Seed"}}}`, secret, secret)
	}))
	defer server.Close()
	adapter := NewOpenRouter(secret, "")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: "model", Prompt: "prompt", ExpectedImages: 1})
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %#v", err)
	}
	if strings.Contains(providerErr.Message, secret) || strings.Contains(providerErr.Message, "private.example") || strings.Contains(providerErr.Message, "QUJD") {
		t.Fatalf("unsafe error detail survived: %q", providerErr.Message)
	}
	if !strings.Contains(providerErr.Message, "Seed: invalid image") || !strings.Contains(providerErr.Message, "[url]") || !strings.Contains(providerErr.Message, "[image-data]") {
		t.Fatalf("useful sanitized detail missing: %q", providerErr.Message)
	}
}

func TestOpenRouterAcceptedEmptyResultIsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"generation-1","data":[]}`))
	}))
	defer server.Close()
	adapter := NewOpenRouter("test-key", "")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: "model", Prompt: "prompt", ExpectedImages: 1})
	var providerErr *Error
	if !errors.As(err, &providerErr) || !providerErr.SubmissionUncertain || providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	if got := parseRetryAfter(now.Add(30*time.Second).Format(http.TimeFormat), now); got != 30*time.Second {
		t.Fatalf("parseRetryAfter = %v", got)
	}
}

func TestOpenRouterProbeUsesKeyEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":{"usage":1,"limit":10,"limit_remaining":9}}`))
	}))
	defer server.Close()
	adapter := NewOpenRouter("test-key", "")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	if health := adapter.Probe(context.Background()); !health.Healthy {
		t.Fatalf("health = %+v", health)
	}
}

func TestDecodeOpenRouterResponseIncrementally(t *testing.T) {
	body := `{"id":"generation-2","data":[` +
		`{"b64_json":"cG5n","media_type":"image/png"},` +
		`{"b64_json":"d2VicA==","media_type":"image/webp"}` +
		`],"usage":{"cost":0.02}}`
	reader := &chunkedReader{reader: strings.NewReader(body), size: 7}
	id, images, usage, err := decodeOpenRouterResponse(reader)
	if err != nil {
		t.Fatalf("decodeOpenRouterResponse: %v", err)
	}
	if id != "generation-2" || len(images) != 2 || string(images[0].Bytes) != "png" || string(images[1].Bytes) != "webp" {
		t.Fatalf("id=%q images=%+v", id, images)
	}
	if usage["cost"] != float64(0.02) {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestForbiddenRequestDoesNotPauseWholeProvider(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"error":"content rejected"}`)),
	}
	providerErr, ok := httpProviderError(response).(*Error)
	if !ok {
		t.Fatal("expected provider error")
	}
	if providerErr.PauseProvider || providerErr.Retryable {
		t.Fatalf("403 classification = pause:%v retry:%v", providerErr.PauseProvider, providerErr.Retryable)
	}
}

type chunkedReader struct {
	reader *strings.Reader
	size   int
}

func (r *chunkedReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.size {
		buffer = buffer[:r.size]
	}
	return r.reader.Read(buffer)
}
