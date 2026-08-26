package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenRouterPromptOptimizerSendsIsolatedStrictRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer refiner-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != OpenRouterPromptRefinerModel {
			t.Fatalf("model = %#v", payload["model"])
		}
		reasoning, _ := payload["reasoning"].(map[string]any)
		if reasoning["exclude"] != true || reasoning["effort"] != "low" {
			t.Fatalf("reasoning = %#v", payload["reasoning"])
		}
		providerPolicy, _ := payload["provider"].(map[string]any)
		if providerPolicy["require_parameters"] != true || providerPolicy["data_collection"] != "deny" || payload["stream"] != false || payload["n"] != float64(1) {
			t.Fatalf("routing controls = provider:%#v stream:%#v n:%#v", providerPolicy, payload["stream"], payload["n"])
		}
		if payload["max_tokens"] != float64(1280) {
			t.Fatalf("max_tokens = %#v", payload["max_tokens"])
		}
		format, _ := payload["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Fatalf("response_format = %#v", payload["response_format"])
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 2 || !strings.Contains(messages[1].(map[string]any)["content"].(string), `"prompt":"original prompt"`) {
			t.Fatalf("messages = %#v", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"prompt\":\"safer prompt\"}","reasoning":"must be ignored"}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}`))
	}))
	defer server.Close()

	optimizer := NewOpenRouterPromptOptimizer([]string{"refiner-key"}, "https://cornfield.test", time.Second)
	optimizer.BaseURL = server.URL
	optimizer.Client = server.Client()
	result, err := optimizer.Optimize(context.Background(), PromptOptimizationRequest{
		Prompt: "original prompt", TargetProvider: "legnext", TargetModel: "midjourney", MaxRunes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Prompt != "safer prompt" || result.TotalTokens != 16 {
		t.Fatalf("result = %#v", result)
	}
}

func TestPromptRefinerMaxTokens(t *testing.T) {
	for _, testCase := range []struct{ runes, want int }{
		{1, 512}, {1024, 1280}, {8192, 8448}, {32_768, 12_288},
	} {
		if got := promptRefinerMaxTokens(testCase.runes); got != testCase.want {
			t.Fatalf("promptRefinerMaxTokens(%d)=%d want %d", testCase.runes, got, testCase.want)
		}
	}
}

func TestOpenRouterPromptOptimizerFailsOverOnlyDefiniteCredentialFailures(t *testing.T) {
	var keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		keys = append(keys, key)
		if key == "key-a" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"prompt\":\"filtered\"}"}}]}`))
	}))
	defer server.Close()
	optimizer := NewOpenRouterPromptOptimizer([]string{"key-a", "key-b"}, "", time.Second)
	optimizer.BaseURL = server.URL
	optimizer.Client = server.Client()
	result, err := optimizer.Optimize(context.Background(), PromptOptimizationRequest{Prompt: "original", MaxRunes: 100})
	if err != nil || result.Prompt != "filtered" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if strings.Join(keys, ",") != "key-a,key-b" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestDecodeStrictPromptObject(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		content string
		want    string
		ok      bool
	}{
		{name: "valid", content: ` {"prompt":"quiet field"} `, want: "quiet field", ok: true},
		{name: "unknown", content: `{"prompt":"field","analysis":"x"}`},
		{name: "duplicate", content: `{"prompt":"one","prompt":"two"}`},
		{name: "trailing", content: `{"prompt":"field"} text`},
		{name: "bare", content: `quiet field`},
		{name: "empty", content: `{"prompt":""}`},
		{name: "control", content: "{\"prompt\":\"field\\u0000\"}"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := decodeStrictPromptObject(testCase.content)
			if (err == nil) != testCase.ok || got != testCase.want {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}
}

func TestOpenRouterPromptOptimizerRejectsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"{\"prompt\":\"field\"}","tool_calls":[{"id":"unexpected"}]}}]}`))
	}))
	defer server.Close()
	optimizer := NewOpenRouterPromptOptimizer([]string{"key"}, "", time.Second)
	optimizer.BaseURL = server.URL
	optimizer.Client = server.Client()
	_, err := optimizer.Optimize(context.Background(), PromptOptimizationRequest{Prompt: "field", MaxRunes: 100})
	if err == nil {
		t.Fatal("expected tool call rejection")
	}
}

func TestOpenRouterPromptOptimizerRejectsOversizedAndTruncatedResults(t *testing.T) {
	for name, response := range map[string]string{
		"over budget": `{"choices":[{"finish_reason":"stop","message":{"content":"{\"prompt\":\"too long\"}"}}]}`,
		"truncated":   `{"choices":[{"finish_reason":"length","message":{"content":"{\"prompt\":\"field\"}"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			optimizer := NewOpenRouterPromptOptimizer([]string{"key"}, "", time.Second)
			optimizer.BaseURL = server.URL
			optimizer.Client = server.Client()
			_, err := optimizer.Optimize(context.Background(), PromptOptimizationRequest{Prompt: "field", MaxRunes: 5})
			if err == nil {
				t.Fatal("expected invalid response")
			}
		})
	}
}
