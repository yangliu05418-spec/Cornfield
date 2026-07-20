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
	if textCases != 222 {
		t.Fatalf("text matrix contains %d cases, want 222", textCases)
	}
	if imageCases != 8 {
		t.Fatalf("image smoke matrix contains %d cases, want 8", imageCases)
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
