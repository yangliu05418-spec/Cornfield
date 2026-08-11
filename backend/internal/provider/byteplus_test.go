package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBytePlusSubmitUsesOfficialProContract(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("X-Request-Id", "request-1")
		_, _ = w.Write([]byte(`{"model":"dola-seedream-5-0-pro-260628","data":[{"b64_json":"cG5n","output_format":"png","size":"2048x2048"}],"usage":{"generated_images":1,"input_images":2,"output_tokens":16384,"total_tokens":16384}}`))
	}))
	defer server.Close()

	adapter := NewBytePlus("test-key")
	adapter.BaseURL, adapter.Client = server.URL, server.Client()
	submission, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model: "dola-seedream-5-0-pro-260628", Prompt: "quiet cornfield", Size: "2048x2048", ExpectedImages: 1,
		ReferenceURLs: []string{"https://cornfield.test/a.png", "https://cornfield.test/b.png"},
		Options:       GenerationOptions{Image: &ImageOptions{PromptOptimizationMode: "fast"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !submission.Completed || submission.ProviderJobID != "request-1" || len(submission.Result.Images) != 1 || string(submission.Result.Images[0].Bytes) != "png" {
		t.Fatalf("unexpected submission: %+v", submission)
	}
	if submission.Result.Usage["generated_images"] != float64(1) || submission.Result.Usage["input_images"] != float64(2) || submission.Result.Usage["output_tokens"] != float64(16384) {
		t.Fatalf("usage was not preserved: %#v", submission.Result.Usage)
	}
	if received["response_format"] != "b64_json" || received["output_format"] != "png" || received["watermark"] != false || received["size"] != "2048x2048" {
		t.Fatalf("unexpected fixed parameters: %#v", received)
	}
	optimization, ok := received["optimize_prompt_options"].(map[string]any)
	if !ok || optimization["mode"] != "fast" {
		t.Fatalf("unexpected prompt options: %#v", received["optimize_prompt_options"])
	}
	images, ok := received["image"].([]any)
	if !ok || len(images) != 2 {
		t.Fatalf("unexpected references: %#v", received["image"])
	}
	for _, forbidden := range []string{"stream", "sequential_image_generation", "sequential_image_generation_options", "seed", "guidance_scale"} {
		if _, exists := received[forbidden]; exists {
			t.Fatalf("forbidden field %q was sent", forbidden)
		}
	}
}

func TestBytePlusSubmitOmitsReferencesAndDefaultsStandard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, exists := payload["image"]; exists {
			t.Fatal("empty image field was sent")
		}
		if payload["optimize_prompt_options"].(map[string]any)["mode"] != "standard" {
			t.Fatalf("unexpected optimization: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"cG5n"}]}`))
	}))
	defer server.Close()
	adapter := NewBytePlus("test-key")
	adapter.BaseURL, adapter.Client = server.URL, server.Client()
	if _, err := adapter.Submit(context.Background(), CanonicalRequest{Model: bytePlusProbeModel, Prompt: "test", Size: "1024x1024", ExpectedImages: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestBytePlusRejectsUnsupportedInputsLocally(t *testing.T) {
	adapter := NewBytePlus("test-key")
	base := CanonicalRequest{Model: bytePlusProbeModel, Prompt: "test", Size: "1024x1024", ExpectedImages: 1}
	tests := []struct {
		name   string
		mutate func(*CanonicalRequest)
	}{
		{name: "multiple outputs", mutate: func(input *CanonicalRequest) { input.ExpectedImages = 2 }},
		{name: "missing size", mutate: func(input *CanonicalRequest) { input.Size = "" }},
		{name: "invalid mode", mutate: func(input *CanonicalRequest) { input.Options.Image = &ImageOptions{PromptOptimizationMode: "turbo"} }},
		{name: "eleven references", mutate: func(input *CanonicalRequest) { input.ReferenceURLs = make([]string, 11) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := adapter.Submit(context.Background(), input); err == nil {
				t.Fatal("unsupported input was accepted")
			}
		})
	}
}

func TestBytePlusErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		code      string
		retryable bool
		pause     bool
		uncertain bool
	}{
		{name: "parameter", status: 400, body: `{"error":{"code":"InvalidParameter.size","message":"invalid size"}}`, code: "PROVIDER_HTTP_400"},
		{name: "policy", status: 403, body: `{"error":{"code":"ContentFilter","message":"content policy rejected"}}`, code: "CONTENT_POLICY_REJECTED"},
		{name: "authentication", status: 401, body: `{"error":{"code":"AuthenticationError","message":"invalid key"}}`, code: "PROVIDER_HTTP_401", pause: true},
		{name: "rate", status: 429, body: `{"error":{"code":"RequestLimitExceeded","message":"busy"}}`, code: "PROVIDER_HTTP_429", retryable: true},
		{name: "ambiguous server", status: 500, body: `{"error":{"code":"InternalError","message":"failed"}}`, code: "SUBMISSION_UNCERTAIN", uncertain: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			adapter := NewBytePlus("test-key")
			adapter.BaseURL, adapter.Client = server.URL, server.Client()
			_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: bytePlusProbeModel, Prompt: "private prompt", Size: "1024x1024", ExpectedImages: 1})
			providerErr, ok := err.(*Error)
			if !ok || providerErr.Code != test.code || providerErr.Retryable != test.retryable || providerErr.PauseProvider != test.pause || providerErr.SubmissionUncertain != test.uncertain {
				t.Fatalf("error = %#v", err)
			}
			if strings.Contains(providerErr.Message, "private prompt") || strings.Contains(providerErr.Message, "test-key") {
				t.Fatalf("secret detail leaked: %q", providerErr.Message)
			}
		})
	}
}

func TestBytePlusItemErrorAndInvalidBase64(t *testing.T) {
	tests := []struct {
		body string
		code string
	}{
		{body: `{"data":[{"error":{"code":"ContentFilter","message":"sensitive content"}}]}`, code: "CONTENT_POLICY_REJECTED"},
		{body: `{"data":[{"b64_json":"not-base64"}]}`, code: "SUBMISSION_UNCERTAIN"},
	}
	for _, test := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(test.body)) }))
		adapter := NewBytePlus("test-key")
		adapter.BaseURL, adapter.Client = server.URL, server.Client()
		_, err := adapter.Submit(context.Background(), CanonicalRequest{Model: bytePlusProbeModel, Prompt: "test", Size: "1024x1024", ExpectedImages: 1})
		server.Close()
		providerErr, ok := err.(*Error)
		if !ok || providerErr.Code != test.code {
			t.Fatalf("error = %#v, want %s", err, test.code)
		}
	}
}

func TestBytePlusProbeUsesMissingPromptAndCaches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"MissingParameter.prompt","message":"prompt is required","param":"prompt"}}`))
	}))
	defer server.Close()
	adapter := NewBytePlus("test-key")
	adapter.BaseURL, adapter.Client = server.URL, server.Client()
	adapter.ProbeInterval = time.Hour
	if health := adapter.Probe(context.Background()); !health.Healthy {
		t.Fatalf("probe = %+v", health)
	}
	if health := adapter.Probe(context.Background()); !health.Healthy || calls.Load() != 1 {
		t.Fatalf("cached probe = %+v, calls=%d", health, calls.Load())
	}
}
