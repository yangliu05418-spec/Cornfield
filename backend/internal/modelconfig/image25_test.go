package modelconfig

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestImage25Capabilities(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"sunburst", "flare"} {
		m, ok := catalog.Find("openrouter-gpt-image-2-5-" + variant)
		if !ok || !m.Enabled || m.ProviderModel != "openai/gpt-image-2.5-"+variant || m.PromptAspectRatio || m.OutputsPerDraw != 1 {
			t.Fatalf("incorrect model: %+v", m)
		}
		if !slices.Equal(m.Capabilities.Qualities, []string{"auto", "low", "medium", "high", "xhigh", "max"}) || len(m.Capabilities.AspectRatios) != 9 || !slices.Contains(m.Capabilities.AspectRatios, "auto") || len(m.Capabilities.Resolutions) != 0 || m.Capabilities.MaxReferenceImages != 16 {
			t.Fatalf("incorrect capabilities: %+v", m.Capabilities)
		}
		m.Capabilities.AspectRatios = append(m.Capabilities.AspectRatios, "auto")
		if validateCapabilities(m) == nil {
			t.Fatal("duplicate auto accepted")
		}
	}
}
