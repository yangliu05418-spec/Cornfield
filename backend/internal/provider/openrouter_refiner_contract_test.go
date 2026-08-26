package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"internal-image-studio/internal/refinercanary"
)

func TestOpenRouterPromptOptimizerSyntheticContractCorpus(t *testing.T) {
	fixtures, err := refinercanary.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.ID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch fixture.Transport {
				case "http_429":
					w.WriteHeader(http.StatusTooManyRequests)
					return
				case "http_500":
					w.WriteHeader(http.StatusInternalServerError)
					return
				case "timeout":
					time.Sleep(30 * time.Millisecond)
					return
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", maxPromptRefinerResponse+1)))
					return
				}
				response := map[string]any{
					"choices": []any{map[string]any{
						"finish_reason": fixture.FinishReason,
						"message": map[string]any{
							"content":   fixture.RawContent,
							"reasoning": "synthetic text that must be ignored",
						},
					}},
					"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()

			optimizer := NewOpenRouterPromptOptimizer([]string{"synthetic-key"}, "https://cornfield.test", time.Second)
			optimizer.BaseURL = server.URL
			ctx := context.Background()
			if fixture.Transport == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			result, optimizeErr := optimizer.Optimize(ctx, PromptOptimizationRequest{
				Prompt: fixture.Original, TargetProvider: fixture.TargetProvider,
				TargetModel: fixture.TargetModel, MaxRunes: fixture.MaxRunes,
			})
			switch fixture.ExpectedProvider {
			case "accept":
				if optimizeErr != nil {
					t.Fatalf("Optimize() error = %v", optimizeErr)
				}
				if result.Prompt != strings.TrimSpace(fixture.Candidate) {
					t.Fatalf("candidate differs from fixture contract")
				}
				invariantErr := refinercanary.ValidateInvariant(fixture, result.Prompt)
				if fixture.ExpectedInvariant == "accept" && invariantErr != nil {
					t.Fatalf("candidate invariant rejected: %v", invariantErr)
				}
				if fixture.ExpectedInvariant == "reject" && invariantErr == nil {
					t.Fatal("candidate invariant unexpectedly accepted")
				}
			case "invalid_response", "http_error":
				var typed *Error
				if !errors.As(optimizeErr, &typed) {
					t.Fatalf("Optimize() error = %v, want provider Error", optimizeErr)
				}
				wantCode := fixture.ExpectedCode
				if wantCode == "" {
					wantCode = "PROMPT_REFINER_INVALID_RESPONSE"
				}
				if typed.Code != wantCode {
					t.Fatalf("error code = %q, want %q", typed.Code, wantCode)
				}
			default:
				t.Fatalf("unknown expected provider result %q", fixture.ExpectedProvider)
			}
		})
	}
}

func TestOpenRouterPromptOptimizerNeverIncludesPromptInProviderErrors(t *testing.T) {
	const secretPrompt = "SYNTHETIC_PROMPT_MUST_NOT_LEAK_5f85"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(secretPrompt))
	}))
	defer server.Close()
	optimizer := NewOpenRouterPromptOptimizer([]string{"synthetic-key"}, "", time.Second)
	optimizer.BaseURL = server.URL
	_, err := optimizer.Optimize(context.Background(), PromptOptimizationRequest{Prompt: secretPrompt, MaxRunes: 8192})
	if err == nil || strings.Contains(err.Error(), secretPrompt) {
		t.Fatalf("provider error leaked prompt: %v", err)
	}
	var typed *Error
	if !errors.As(err, &typed) || strings.Contains(typed.Message, secretPrompt) {
		t.Fatalf("typed provider error leaked prompt: %#v", typed)
	}
}
