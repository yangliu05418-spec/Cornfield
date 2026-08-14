package main

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
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

func TestValidateLayerProtocolResult(t *testing.T) {
	base := encodeTestPNG(t, solidTestImage(8, 8, color.NRGBA{R: 20, G: 30, B: 40, A: 255}))
	layerImage := solidTestImage(4, 4, color.NRGBA{R: 255, A: 0})
	layerImage.SetNRGBA(1, 1, color.NRGBA{R: 255, A: 255})
	layer := encodeTestPNG(t, layerImage)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/base.png" {
			_, _ = w.Write(base)
			return
		}
		_, _ = w.Write(layer)
	}))
	defer server.Close()
	items := []provider.LayerDecompositionItem{
		{URL: server.URL + "/base.png", ZIndex: 0, MediaType: "image/png"},
		{URL: server.URL + "/layer.png", ZIndex: 1, MediaType: "image/png", Name: "shape", Description: "red shape", BoundingBox: &provider.LayerBoundingBox{Absolute: [4]int{2, 2, 6, 6}, Normalized: [4]float64{250, 250, 750, 750}}},
	}
	// The production validator deliberately rejects this local host before any
	// network request, proving protocol artifacts cannot bypass the same host gate.
	result := layerProtocolResult{}
	err := validateLayerProtocolResult(server.Client(), t.TempDir(), provider.LayerDecompositionResult{Items: items, Usage: map[string]any{"generated_images": 2}}, &result)
	if err == nil || err.Error() != "output URL is outside the BytePlus allowlist" {
		t.Fatalf("validation error = %v", err)
	}
}

func TestLayerProtocolPosterIsDeterministic2048PNG(t *testing.T) {
	first, err := layerProtocolPoster()
	if err != nil {
		t.Fatal(err)
	}
	second, err := layerProtocolPoster()
	if err != nil {
		t.Fatal(err)
	}
	if hashText(string(first)) != hashText(string(second)) {
		t.Fatal("poster is not deterministic")
	}
	decoded, err := png.Decode(bytes.NewReader(first))
	if err != nil || decoded.Bounds().Dx() != 2048 || decoded.Bounds().Dy() != 2048 {
		t.Fatalf("poster bounds = %v, error = %v", decoded.Bounds(), err)
	}
}

func TestWriteLayerProtocolReportDoesNotContainPromptOrSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	report := layerProtocolReport{Model: "model", Results: []layerProtocolResult{{Name: "auto", Status: "passed", ProviderRequestID: "request"}}}
	if err := writeLayerProtocolReport(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"prompt_text", "base64,", "signed_url", "api_key", "authorization"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("report contains %q: %s", forbidden, text)
		}
	}
}

func TestWriteLayerE2EReportDoesNotContainPromptOrSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	operationID := uuid.New()
	report := layerE2EReport{
		ReleaseSHA: "release",
		Results: []layerE2EResult{{
			Name: "auto-standard", Status: "passed", OperationID: &operationID,
		}},
	}
	if err := writePrivateJSON(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt_text", "base64,", "signed_url", "api_key", "authorization", "password"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("report contains %q: %s", forbidden, data)
		}
	}
}

func TestDocumentFromLayerSetUsesBoundingBoxes(t *testing.T) {
	set := layerSet{
		ID: uuid.New(), BaseAsset: layerAsset{ID: uuid.New(), Width: 2048, Height: 1024},
		Items: []layerSetItem{{
			ID: uuid.New(), ZIndex: 1, Asset: layerAsset{ID: uuid.New(), Width: 200, Height: 100},
			BoundingBoxAbsolute: []int{100, 50, 500, 250},
		}},
	}
	document := documentFromLayerSet(set)
	if document.Canvas.Width != 2048 || document.Canvas.Height != 1024 || len(document.Objects) != 2 {
		t.Fatalf("document = %+v", document)
	}
	want := [6]float64{2, 0, 0, 2, 100, 50}
	if document.Objects[1].Transform != want || document.Objects[1].ZIndex != 1 {
		t.Fatalf("layer transform = %v, want %v", document.Objects[1].Transform, want)
	}
}

func solidTestImage(width, height int, value color.NRGBA) *image.NRGBA {
	imageValue := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			imageValue.SetNRGBA(x, y, value)
		}
	}
	return imageValue
}

func encodeTestPNG(t *testing.T, value image.Image) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
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
	if textCases != 298 {
		t.Fatalf("text matrix contains %d cases, want 298", textCases)
	}
	if imageCases != 11 {
		t.Fatalf("image smoke matrix contains %d cases, want 11", imageCases)
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

func TestApplyPromptOverridePreservesImageInstruction(t *testing.T) {
	groups := [][]canaryCase{{
		{Mode: "text", Prompt: "old", PromptSHA256: hashText("old")},
		{Mode: "image", Prompt: "old", PromptSHA256: hashText("old")},
	}}
	applyPromptOverride(groups, "reference prompt")

	if groups[0][0].Prompt != "reference prompt" {
		t.Fatalf("text prompt = %q", groups[0][0].Prompt)
	}
	imagePrompt := "reference prompt Preserve the reference image's character identity and visual design."
	if groups[0][1].Prompt != imagePrompt {
		t.Fatalf("image prompt = %q", groups[0][1].Prompt)
	}
	if groups[0][0].PromptSHA256 != hashText("reference prompt") || groups[0][1].PromptSHA256 != hashText(imagePrompt) {
		t.Fatal("prompt hashes were not refreshed")
	}
}
