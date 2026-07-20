package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBFLSubmitAndPoll(t *testing.T) {
	var submitted map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-key") != "test-key" {
			t.Fatalf("missing BFL key header")
		}
		switch r.URL.Path {
		case "/v1/flux-2-max":
			if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"id":"job-1","polling_url":"https://api.bfl.ai/v1/get_result?id=job-1"}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	adapter := NewBFL("test-key")
	adapter.BaseURL = server.URL
	adapter.Client = server.Client()
	submission, err := adapter.Submit(context.Background(), CanonicalRequest{
		Model: "flux-2-max", Prompt: "cornfield", AspectRatio: "16:9", Resolution: "2MP",
		ReferenceURLs: []string{"https://cornfield.test/reference"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if submission.ProviderJobID != "job-1" || submission.PollingURL == "" {
		t.Fatalf("submission = %+v", submission)
	}
	if submitted["input_image"] != "https://cornfield.test/reference" || submitted["output_format"] != "png" {
		t.Fatalf("payload = %#v", submitted)
	}
	width, height := int(submitted["width"].(float64)), int(submitted["height"].(float64))
	if width <= height || width*height > 2_000_000 {
		t.Fatalf("dimensions = %dx%d", width, height)
	}
}

func TestBFLDimensionsAndPollingURLValidation(t *testing.T) {
	for _, tier := range []string{"1MP", "2MP", "4MP"} {
		width, height, err := bflDimensions("9:16", tier)
		if err != nil || width%16 != 0 || height%16 != 0 || width >= height || width*height > 4_000_000 {
			t.Fatalf("%s = %dx%d, %v", tier, width, height, err)
		}
	}
	if validBFLPollingURL("https://api.bfl.ai.example.com/v1/get_result?id=1") ||
		validBFLPollingURL("https://api.eu2.bfl.ai.example.com/v1/get_result?id=1") ||
		validBFLPollingURL("https://api.eu2.bfl.ai:8443/v1/get_result?id=1") ||
		validBFLPollingURL("https://api.bfl.ai/"+strings.Repeat("x", 2048)) ||
		!validBFLPollingURL("https://api.eu.bfl.ai/v1/get_result?id=1") ||
		!validBFLPollingURL("https://api.eu2.bfl.ai/v1/get_result?id=1") {
		t.Fatal("polling URL allowlist is incorrect")
	}
}
