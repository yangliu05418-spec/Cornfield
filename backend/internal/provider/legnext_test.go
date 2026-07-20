package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegnextSubmitPrependsReferenceURLs(t *testing.T) {
	var payload struct {
		Text     string `json:"text"`
		Callback string `json:"callback"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/diffusion" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Errorf("x-api-key = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		w.Header().Set("X-Request-Id", "legnext-request-1")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"pending"}`))
	}))
	defer server.Close()

	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	submission, err := adapter.Submit(context.Background(), CanonicalRequest{
		Prompt:        "a quiet farmhouse --v 7 --fast",
		AspectRatio:   "16:9",
		ReferenceURLs: []string{"https://cornfield.test/provider-input/opaque-token"},
		ReferenceData: []string{"data:image/png;base64,must-not-be-sent"},
		CallbackURL:   "https://cornfield.test/api/v1/provider-callbacks/legnext/token",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if submission.ProviderJobID != "job-1" {
		t.Errorf("submission = %+v", submission)
	}
	if submission.Telemetry.ProviderRequestID != "legnext-request-1" || submission.Telemetry.HTTPStatus != http.StatusOK {
		t.Errorf("telemetry = %+v", submission.Telemetry)
	}
	want := "https://cornfield.test/provider-input/opaque-token a quiet farmhouse --v 7 --fast --ar 16:9"
	if payload.Text != want {
		t.Errorf("text = %q, want %q", payload.Text, want)
	}
	if payload.Callback == "" {
		t.Error("callback was omitted")
	}
}

func TestLegnextSubmitBuildsValidatedMidjourneyOptions(t *testing.T) {
	var text string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		text = payload.Text
		_, _ = w.Write([]byte(`{"job_id":"job-options","status":"pending"}`))
	}))
	defer server.Close()
	quality, weight := 4, 1.5
	adapter := NewLegnext("test-key")
	adapter.BaseURL, adapter.Client = server.URL, server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{
		Prompt: "field at dusk", AspectRatio: "16:9",
		ReferenceURLs: []string{"https://cornfield.test/reference"},
		Options: GenerationOptions{Midjourney: &MidjourneyOptions{
			Version: "7", Speed: "turbo", Quality: &quality, Stylize: 200,
			Chaos: 12, Weird: 40, Raw: true, Tile: true, ImageWeight: &weight,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cornfield.test/reference field at dusk --ar 16:9 --q 4 --stylize 200 --chaos 12 --weird 40 --iw 1.5 --turbo --v 7 --raw --tile"
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestLegnextRejectsNonHTTPSReferenceURL(t *testing.T) {
	adapter := NewLegnext("test-key")
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Prompt: "prompt", ReferenceURLs: []string{"http://localhost/image.png"}})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Code != "REFERENCE_URL_INVALID" {
		t.Fatalf("error = %#v", err)
	}
}

func TestLegnextAcceptedResponseWithoutJobIDIsUncertain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"pending"}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Prompt: "prompt"})
	var providerErr *Error
	if !errors.As(err, &providerErr) || !providerErr.SubmissionUncertain || providerErr.Retryable {
		t.Fatalf("error = %#v", err)
	}
}

func TestLegnextSubmitAcceptsTaskIDContract(t *testing.T) {
	for name, response := range map[string]string{
		"task_id":     `{"task_id":"task-1","status":"pending"}`,
		"nested_data": `{"code":200,"data":{"task_id":"task-2"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			adapter := NewLegnext("test-key")
			adapter.BaseURL = server.URL
			adapter.Client = server.Client()
			submission, err := adapter.Submit(context.Background(), CanonicalRequest{Prompt: "prompt"})
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if submission.ProviderJobID == "" {
				t.Fatal("provider job id is empty")
			}
		})
	}
}

func TestLegnextProbeUsesBalanceEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/account/balance" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":200,"data":{"balance_usd":1,"available_credits":10,"available_points":0}}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	if health := adapter.Probe(context.Background()); !health.Healthy {
		t.Fatalf("health = %+v", health)
	}
}

func TestLegnextPollCapturesResponseTelemetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/job/job-1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("X-Request-Id", "legnext-poll-1")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"processing"}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	result, err := adapter.Poll(context.Background(), Submission{ProviderJobID: "job-1"})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.Telemetry.ProviderRequestID != "legnext-poll-1" || result.Telemetry.HTTPStatus != http.StatusOK {
		t.Fatalf("telemetry = %+v", result.Telemetry)
	}
}

func TestLegnextPollDoesNotExposeProviderErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"failed","error":{"message":"data:image/png;base64,must-not-survive"}}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	result, err := adapter.Poll(context.Background(), Submission{ProviderJobID: "job-1"})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.ErrorText != "Legnext generation failed" {
		t.Fatalf("error text = %q", result.ErrorText)
	}
}

func TestLegnextForbiddenIsPerRequestContentPolicyFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"sensitive content"}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Prompt: "test", AspectRatio: "1:1"})
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("Submit error = %v", err)
	}
	if providerErr.Code != "CONTENT_POLICY_REJECTED" || providerErr.PauseProvider || providerErr.Retryable {
		t.Fatalf("403 classification = %+v", providerErr)
	}
}

func TestLegnextForbiddenPermissionFailurePausesProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"Sensitive content detected or permission denied"}`))
	}))
	defer server.Close()
	adapter := NewLegnext("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	_, err := adapter.Submit(context.Background(), CanonicalRequest{Prompt: "test", AspectRatio: "1:1"})
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("Submit error = %v", err)
	}
	if providerErr.Code != "PROVIDER_HTTP_403" || !providerErr.PauseProvider || providerErr.Retryable {
		t.Fatalf("403 classification = %+v", providerErr)
	}
}
