package modelconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPolicyJSONUsesSnakeCaseAndAcceptsLegacySnapshots(t *testing.T) {
	expected := validOpenRouterModel().Policy
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "GenerationTimeoutSeconds") || !strings.Contains(string(encoded), `"generation_timeout_seconds"`) {
		t.Fatalf("policy JSON is not canonical: %s", encoded)
	}

	legacy := []byte(`{"SubmitTimeoutSeconds":30,"GenerationTimeoutSeconds":300,"MaxConcurrency":2,"MaxSafeRetries":2,"BreakerMinRequests":10,"BreakerFailureRatio":0.5,"BreakerCooldownSeconds":30,"AllowedOutputHosts":null}`)
	var decoded Policy
	if err := json.Unmarshal(legacy, &decoded); err != nil {
		t.Fatal(err)
	}
	decodedJSON, _ := json.Marshal(decoded)
	if string(decodedJSON) != string(encoded) {
		t.Fatalf("legacy policy decoded as %s, want %s", decodedJSON, encoded)
	}
}

func TestNormalizeSnapshotJSONCanonicalizesNestedObjectsAndLegacyPolicyKeys(t *testing.T) {
	legacy := []byte(`{"id":"model","unknown":{"nested":{"z":1,"a":2},"keep":true},"policy":{"GenerationTimeoutSeconds":900}}`)
	current := []byte(`{"policy":{"generation_timeout_seconds":900},"unknown":{"keep":true,"nested":{"a":2,"z":1}},"id":"model"}`)
	normalizedLegacy, err := NormalizeSnapshotJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	normalizedCurrent, err := NormalizeSnapshotJSON(current)
	if err != nil {
		t.Fatal(err)
	}
	if string(normalizedLegacy) != string(normalizedCurrent) {
		t.Fatalf("normalized snapshots differ: %s / %s", normalizedLegacy, normalizedCurrent)
	}
	conflict := []byte(`{"policy":{"GenerationTimeoutSeconds":900,"generation_timeout_seconds":300}}`)
	if _, err := NormalizeSnapshotJSON(conflict); err == nil {
		t.Fatal("conflicting policy aliases were accepted")
	}
}

func TestSnapshotJSONEqualUsesJSONBSemantics(t *testing.T) {
	left := []byte(`{"ratio":1e-7,"limit":26214400,"values":[1,2],"policy":{"GenerationTimeoutSeconds":900}}`)
	right := []byte(`{"policy":{"generation_timeout_seconds":900},"values":[1.0,2.0],"limit":26214400.0,"ratio":0.0000001}`)
	equal, err := SnapshotJSONEqual(left, right)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("jsonb-equivalent numeric representations did not match")
	}

	changedArray := []byte(`{"ratio":0.0000001,"limit":26214400,"values":[2,1],"policy":{"generation_timeout_seconds":900}}`)
	equal, err = SnapshotJSONEqual(left, changedArray)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("array order change was accepted")
	}
}

func TestProductionCatalogIsValid(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatalf("Load production catalog: %v", err)
	}
	if len(catalog.Models) != 11 || catalog.Hash == "" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	flash, ok := catalog.Find("openrouter-gemini-3-1-flash-image")
	if !ok || slices.Contains(flash.AspectRatiosForResolution("4K"), "1:4") || !slices.Contains(flash.AspectRatiosForResolution("2K"), "1:4") {
		t.Fatalf("unexpected Nano Banana 2 resolution-specific ratios: %+v", flash.Capabilities.AspectRatiosByResolution)
	}
}

func TestCatalogRejectsIncoherentProviderCapabilities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Model)
		want   string
	}{
		{
			name: "image to image without request parameter",
			mutate: func(model *Model) {
				model.RequestParameters = []string{"n"}
			},
			want: "input_references",
		},
		{
			name: "selectable ratios without provider parameter",
			mutate: func(model *Model) {
				model.Capabilities.AspectRatios = []string{"1:1", "16:9"}
			},
			want: "selectable aspect ratios",
		},
		{
			name: "selectable resolutions without provider parameter",
			mutate: func(model *Model) {
				model.Capabilities.Resolutions = []string{"1K", "2K"}
			},
			want: "selectable resolutions",
		},
		{
			name: "resolution-specific ratios for unknown resolution",
			mutate: func(model *Model) {
				model.RequestParameters = []string{"aspect_ratio", "resolution", "n", "input_references"}
				model.Capabilities.AspectRatios = []string{"1:1"}
				model.Capabilities.Resolutions = []string{"1K"}
				model.Capabilities.AspectRatiosByResolution = map[string][]string{"4K": {"1:1"}}
			},
			want: "unknown resolution",
		},
		{
			name: "multiple outputs without n",
			mutate: func(model *Model) {
				model.OutputsPerDraw = 2
				model.RequestParameters = []string{"input_references"}
			},
			want: "without OpenRouter n",
		},
		{
			name: "invalid circuit breaker",
			mutate: func(model *Model) {
				model.Policy.BreakerFailureRatio = 1.5
			},
			want: "invalid breaker policy",
		},
		{
			name: "invalid ratio syntax",
			mutate: func(model *Model) {
				model.Capabilities.AspectRatios = []string{"auto"}
			},
			want: "invalid aspect ratio",
		},
		{
			name: "image to image without byte limit",
			mutate: func(model *Model) {
				model.Capabilities.MaxReferenceBytes = 0
			},
			want: "valid reference image capacity",
		},
		{
			name: "reference byte limit above upload ceiling",
			mutate: func(model *Model) {
				model.Capabilities.MaxReferenceBytes = 25<<20 + 1
			},
			want: "valid reference image capacity",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := validOpenRouterModel()
			test.mutate(&model)
			err := (Catalog{Revision: 1, Models: []Model{model}}).Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCatalogHashIncludesOperationalPolicy(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "..", "config", "models.yaml")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(t.TempDir(), "first.yaml")
	secondPath := filepath.Join(t.TempDir(), "second.yaml")
	if err := os.WriteFile(firstPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(string(source), "max_concurrency: 4", "max_concurrency: 3")
	if err := os.WriteFile(secondPath, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := Load(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == second.Hash {
		t.Fatal("capability hash did not change with operational policy")
	}
}

func TestProviderConcurrencyUsesOneCatalogValue(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := catalog.ProviderConcurrency()
	if err != nil {
		t.Fatal(err)
	}
	for provider, want := range map[string]int{"legnext": 2, "openrouter": 4, "bfl": 4, "byteplus": 2} {
		if limits[provider] != want {
			t.Fatalf("provider %s limit = %d, want %d", provider, limits[provider], want)
		}
	}

	first := validOpenRouterModel()
	second := validOpenRouterModel()
	second.ID = "openrouter-test-2"
	second.Policy.MaxConcurrency = first.Policy.MaxConcurrency + 1
	if err := (Catalog{Revision: 1, Models: []Model{first, second}}).Validate(); err == nil || !strings.Contains(err.Error(), "inconsistent max_concurrency") {
		t.Fatalf("inconsistent provider limit error = %v", err)
	}
}

func TestMaxSubmitTimeoutUsesEnabledModels(t *testing.T) {
	fast := validOpenRouterModel()
	fast.ID = "fast"
	fast.Policy.SubmitTimeoutSeconds = 20
	slow := validOpenRouterModel()
	slow.ID = "slow"
	slow.Policy.SubmitTimeoutSeconds = 300
	disabled := validOpenRouterModel()
	disabled.ID = "disabled"
	disabled.Enabled = false
	disabled.Policy.SubmitTimeoutSeconds = 900
	catalog := Catalog{Models: []Model{fast, slow, disabled}}
	if got := catalog.MaxSubmitTimeout(); got != 300*time.Second {
		t.Fatalf("MaxSubmitTimeout() = %v, want 300s", got)
	}
}

func TestSeedreamExplicitSizeCatalog(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	model, ok := catalog.Find("openrouter-seedream-4-5")
	if !ok {
		t.Fatal("Seedream model missing")
	}
	if strings.Join(model.Capabilities.Resolutions, ",") != "2K,4K" {
		t.Fatalf("resolutions = %#v", model.Capabilities.Resolutions)
	}
	if got := model.SizeOverrides["2K"]["16:9"]; got != "2560x1440" {
		t.Fatalf("16:9 2K override = %q", got)
	}
	if len(model.SizeOverrides["2K"]) != len(model.Capabilities.AspectRatios) {
		t.Fatalf("size override count = %d, ratios = %d", len(model.SizeOverrides["2K"]), len(model.Capabilities.AspectRatios))
	}
}

func TestSeedreamSizeOverridesRejectUnsafeArea(t *testing.T) {
	model := validOpenRouterModel()
	model.ProviderModel = "bytedance-seed/seedream-4.5"
	model.RequestParameters = []string{"size", "resolution", "aspect_ratio", "n", "input_references"}
	model.Capabilities.AspectRatios = []string{"16:9"}
	model.Capabilities.Resolutions = []string{"2K"}
	model.SizeOverrides = map[string]map[string]string{"2K": {"16:9": "2048x1152"}}
	if err := validateCapabilities(model); err == nil || !strings.Contains(err.Error(), "minimum pixel area") {
		t.Fatalf("validateCapabilities() = %v", err)
	}
}

func TestBytePlusSeedreamCatalog(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	model, ok := catalog.Find("byteplus-seedream-5-0-pro")
	if !ok {
		t.Fatal("BytePlus Seedream 5.0 Pro missing")
	}
	if model.ProviderModel != "dola-seedream-5-0-pro-260628" || model.OutputsPerDraw != 1 || model.Capabilities.MaxReferenceImages != 10 {
		t.Fatalf("unexpected model contract: %+v", model)
	}
	if strings.Join(model.Capabilities.PromptOptimizationModes, ",") != "standard,fast" || len(model.SizeOverrides) != 3 {
		t.Fatalf("unexpected BytePlus capabilities: %+v", model.Capabilities)
	}
	if model.Capabilities.LayerDecomposition || strings.Join(model.Capabilities.LayerDecompositionSizes, ",") != "auto,1K,1.5K,2K" || model.Capabilities.MaxDecompositionLayers != 16 {
		t.Fatalf("unexpected layer decomposition rollout contract: %+v", model.Capabilities)
	}
	if model.Policy.LayerDecompositionTimeoutSeconds != 600 || catalog.MaxOperationTimeout() != 600*time.Second {
		t.Fatalf("operation timeout = %v, policy = %d", catalog.MaxOperationTimeout(), model.Policy.LayerDecompositionTimeoutSeconds)
	}
	expected := map[string]map[string]string{
		"1K": {
			"1:1": "1024x1024", "4:3": "1152x864", "3:4": "864x1152", "16:9": "1424x800",
			"9:16": "800x1424", "3:2": "1248x832", "2:3": "832x1248", "21:9": "1568x672",
		},
		"1.5K": {
			"1:1": "1536x1536", "4:3": "1792x1344", "3:4": "1344x1792", "16:9": "2048x1152",
			"9:16": "1152x2048", "3:2": "1872x1248", "2:3": "1248x1872", "21:9": "2352x1008",
		},
		"2K": {
			"1:1": "2048x2048", "4:3": "2368x1776", "3:4": "1776x2368", "16:9": "2816x1584",
			"9:16": "1584x2816", "3:2": "2496x1664", "2:3": "1664x2496", "21:9": "3136x1344",
		},
	}
	for resolution, sizes := range expected {
		if len(model.SizeOverrides[resolution]) != len(sizes) {
			t.Fatalf("%s size mappings = %d", resolution, len(model.SizeOverrides[resolution]))
		}
		for ratio, size := range sizes {
			if got := model.SizeOverrides[resolution][ratio]; got != size {
				t.Fatalf("%s %s = %q, want %q", resolution, ratio, got, size)
			}
		}
	}
}

func TestBytePlusSizeOverridesRejectUnsafeArea(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	model, _ := catalog.Find("byteplus-seedream-5-0-pro")
	model.SizeOverrides["1K"]["1:1"] = "512x512"
	if err := validateCapabilities(model); err == nil || !strings.Contains(err.Error(), "BytePlus pixel limits") {
		t.Fatalf("validateCapabilities() = %v", err)
	}
}

func validOpenRouterModel() Model {
	return Model{
		ID:                "openrouter-test",
		DisplayName:       "Test",
		Provider:          "openrouter",
		ProviderModel:     "author/model",
		Enabled:           true,
		RequestParameters: []string{"n", "input_references"},
		OutputsPerDraw:    1,
		Capabilities: Capabilities{
			TextToImage:        true,
			ImageToImage:       true,
			AspectRatios:       []string{},
			Resolutions:        []string{},
			MaxReferenceImages: 4,
			MaxReferenceBytes:  25 << 20,
			DrawCount:          DrawCount{Min: 1, Max: 4, Default: 1},
		},
		Policy: Policy{
			SubmitTimeoutSeconds:     30,
			GenerationTimeoutSeconds: 300,
			MaxConcurrency:           2,
			MaxSafeRetries:           2,
			BreakerMinRequests:       10,
			BreakerFailureRatio:      0.5,
			BreakerCooldownSeconds:   30,
		},
	}
}
