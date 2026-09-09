package main

import (
	"github.com/google/uuid"
	"internal-image-studio/internal/modelconfig"
	"path/filepath"
	"testing"
)

func TestImage25CanarySelection(t *testing.T) {
	catalog, err := modelconfig.Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	hash := catalog.Hash
	if err := filterCanaryModels(catalog, "openrouter-gpt-image-2-5-sunburst,openrouter-gpt-image-2-5-flare,openrouter-gpt-image-2-5-flare"); err != nil {
		t.Fatal(err)
	}
	if catalog.Hash != hash || len(catalog.Models) != 2 {
		t.Fatal("selection changed revision or retained duplicates")
	}
	groups := buildCanaryGroups(catalog, "matrix", "release", 42, []uuid.UUID{uuid.New()})
	if len(groups) != 2 || len(groups[0]) != 55 || len(groups[1]) != 55 {
		t.Fatal("expected 108 text cases and two image cases")
	}
	if filterCanaryModels(catalog, "unknown") == nil {
		t.Fatal("unknown model accepted")
	}
	if !ratioMatches(1024, 768, "auto", 0.01) || ratioMatches(0, 768, "auto", 0.01) {
		t.Fatal("invalid auto ratio validation")
	}
}
