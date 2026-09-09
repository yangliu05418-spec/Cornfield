package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"internal-image-studio/internal/modelconfig"
)

// Exercise the production catalog, not a separately maintained test capability.
func TestImage25RequestMatrix(t *testing.T) {
	catalog, err := modelconfig.Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models {
		if !strings.HasPrefix(model.ProviderModel, "openai/gpt-image-2.5-") {
			continue
		}
		for _, ratio := range model.Capabilities.AspectRatios {
			for _, quality := range model.Capabilities.Qualities {
				for _, references := range []int{0, 1, 16} {
					t.Run(model.ID+"/"+ratio+"/"+quality+"/"+strings.Repeat("r", references), func(t *testing.T) {
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Fatal(err)
							}
							if body["model"] != model.ProviderModel || body["aspect_ratio"] != ratio || body["quality"] != quality || body["prompt"] != "a blue teapot" || body["n"] != float64(1) {
								t.Errorf("incorrect request: %#v", body)
							}
							for _, key := range []string{"resolution", "size", "stream", "moderation"} {
								if _, ok := body[key]; ok {
									t.Errorf("unexpected %s", key)
								}
							}
							if references > 0 {
								if refs, ok := body["input_references"].([]any); !ok || len(refs) != references {
									t.Error("reference count mismatch")
								}
							} else if _, ok := body["input_references"]; ok {
								t.Error("unexpected references")
							}
							_, _ = w.Write([]byte(`{"data":[{"b64_json":"cG5n","media_type":"image/png"}]}`))
						}))
						defer server.Close()
						adapter := NewOpenRouter("test-key", "")
						adapter.BaseURL, adapter.Client = server.URL, server.Client()
						refs := make([]string, references)
						for i := range refs {
							refs[i] = "data:image/png;base64,cG5n"
						}
						result, err := adapter.Submit(context.Background(), CanonicalRequest{Model: model.ProviderModel, Prompt: "a blue teapot", AspectRatio: ratio, Resolution: "auto", ExpectedImages: 1, RequestParameters: model.RequestParameters, PromptAspectRatio: model.PromptAspectRatio, ReferenceData: refs, Options: GenerationOptions{Image: &ImageOptions{Quality: quality}}})
						if err != nil || !result.Completed {
							t.Fatalf("submit: %v", err)
						}
					})
				}
			}
		}
	}
}
