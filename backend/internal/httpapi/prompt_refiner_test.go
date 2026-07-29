package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/promptrefiner"
	"internal-image-studio/internal/provider"
)

func testPromptRefinerServer(t *testing.T, model modelconfig.Model) *Server {
	t.Helper()
	engine, err := promptrefiner.New()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{
		catalog:       &modelconfig.Catalog{Hash: "revision", Models: []modelconfig.Model{model}},
		promptRefiner: engine,
	}
}

func TestRefinePromptReportsRulesAndMidjourneyDiagnostics(t *testing.T) {
	server := testPromptRefinerServer(t, modelconfig.Model{
		ID: "legnext-midjourney", Provider: "legnext", ProviderModel: "midjourney", Enabled: true, OutputsPerDraw: 4,
		Capabilities: modelconfig.Capabilities{
			TextToImage: true, AspectRatios: []string{"1:1"}, Resolutions: []string{"SD", "HD"},
			MidjourneyVersions: []string{"8.2"}, DrawCount: modelconfig.DrawCount{Min: 1, Max: 1, Default: 1},
		},
	})
	longPrompt := "blood " + strings.Repeat("quiet field ", 100) + " --raw"
	body, _ := json.Marshal(generationRequest{
		ModelID: "legnext-midjourney", CapabilityRevision: "revision", Prompt: longPrompt,
		AspectRatio: "1:1", Resolution: "SD", DrawCount: 1,
		Options: provider.GenerationOptions{Midjourney: &provider.MidjourneyOptions{
			Version: "8.2", Resolution: "sd", Speed: "fast", Stylize: 100,
		}},
	})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/prompts/refine", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	server.refinePrompt(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result promptRefineResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "findings" || len(result.Findings) != 1 || result.Findings[0].Original != "blood" {
		t.Fatalf("findings=%#v", result.Findings)
	}
	codes := make(map[string]bool)
	for _, item := range result.Diagnostics {
		codes[item.Code] = true
	}
	if !codes["CONTROLLED_PROVIDER_INPUT"] || !codes["MIDJOURNEY_COMPATIBILITY_LIMIT"] {
		t.Fatalf("diagnostics=%#v", result.Diagnostics)
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
}

func TestRefinePromptRejectsStaleRevisionAndOversize(t *testing.T) {
	server := testPromptRefinerServer(t, modelconfig.Model{
		ID: "bfl-flux", Provider: "bfl", Enabled: true,
		Capabilities: modelconfig.Capabilities{DrawCount: modelconfig.DrawCount{Min: 1, Max: 1, Default: 1}},
	})
	for name, testCase := range map[string]struct {
		input generationRequest
		code  int
	}{
		"stale": {
			input: generationRequest{ModelID: "bfl-flux", CapabilityRevision: "old", Prompt: "field"},
			code:  http.StatusConflict,
		},
		"oversize": {
			input: generationRequest{ModelID: "bfl-flux", CapabilityRevision: "revision", Prompt: strings.Repeat("界", maxRefinerRunes+1)},
			code:  http.StatusRequestEntityTooLarge,
		},
	} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(testCase.input)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/prompts/refine", strings.NewReader(string(body)))
			response := httptest.NewRecorder()
			server.refinePrompt(response, request)
			if response.Code != testCase.code {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
