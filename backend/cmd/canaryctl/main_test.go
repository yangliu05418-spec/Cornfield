package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/modelconfig"
)

func TestCreatePermitStreamDoesNotBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interval := 20 * time.Millisecond
	permits := newCreatePermitStream(ctx, interval)
	<-permits
	select {
	case <-permits:
		t.Fatal("received a second create permit before the interval")
	case <-time.After(interval / 2):
	}
	select {
	case <-permits:
	case <-time.After(3 * interval):
		t.Fatal("timed out waiting for the next create permit")
	}
}

func TestProductionCatalogCanaryMatrix(t *testing.T) {
	catalog, err := modelconfig.Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var textCases, imageCases int
	for _, model := range catalog.Models {
		if !model.Enabled {
			continue
		}
		textCases += len(buildTextCases(model, catalog.Hash, "release", 42))
		if model.Capabilities.ImageToImage {
			_ = buildImageCase(model, catalog.Hash, "release", 42, uuid.New())
			imageCases++
		}
	}
	if textCases != 246 {
		t.Fatalf("text matrix contains %d cases, want 246", textCases)
	}
	if imageCases != 9 {
		t.Fatalf("image smoke matrix contains %d cases, want 9", imageCases)
	}
}

func TestLaunchProfileContainsTwentyFourCases(t *testing.T) {
	catalog, err := modelconfig.Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	groups := buildCanaryGroups(catalog, "launch", "release", 42, []uuid.UUID{uuid.New()})
	total := 0
	counts := make(map[string]int)
	for _, group := range groups {
		for _, item := range group {
			total++
			counts[item.Model.ID]++
		}
	}
	if total != 24 || counts["legnext-midjourney"] != 20 || counts["openrouter-gemini-3-1-flash-image"] != 2 || counts["bfl-flux-2-max"] != 2 {
		t.Fatalf("launch profile total=%d counts=%v", total, counts)
	}
}

func TestBytePlusProfileContainsTwentySevenCases(t *testing.T) {
	catalog, err := modelconfig.Load(filepath.Join("..", "..", "..", "config", "models.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	references := make([]uuid.UUID, 10)
	for index := range references {
		references[index] = uuid.New()
	}
	groups := buildCanaryGroups(catalog, "byteplus", "release", 42, references)
	if len(groups) != 1 || len(groups[0]) != 27 {
		t.Fatalf("BytePlus groups = %d, cases = %v", len(groups), groups)
	}
	text, images := 0, 0
	for _, item := range groups[0] {
		if item.Mode == "text" {
			text++
			if item.PromptOptimizationMode != "standard" && item.PromptOptimizationMode != "fast" {
				t.Fatalf("missing prompt mode: %+v", item)
			}
		} else {
			images++
		}
	}
	if text != 24 || images != 3 || len(groups[0][26].ReferenceIDs) != 10 {
		t.Fatalf("BytePlus text=%d image=%d last refs=%d", text, images, len(groups[0][26].ReferenceIDs))
	}
}

func TestRatioValidation(t *testing.T) {
	if !ratioMatches(2560, 1440, "16:9", 0.001) {
		t.Fatal("exact 16:9 size did not match")
	}
	if ratioMatches(2048, 2048, "16:9", 0.05) {
		t.Fatal("square output matched 16:9")
	}
}

func TestPromptsAreDeterministicButCaseSpecific(t *testing.T) {
	first := randomPrompt(42, "model|text|2K|16:9|", false)
	if first != randomPrompt(42, "model|text|2K|16:9|", false) {
		t.Fatal("same release and case produced different prompts")
	}
	if first == randomPrompt(42, "model|text|2K|1:1|", false) {
		t.Fatal("different cases produced identical prompts")
	}
}
