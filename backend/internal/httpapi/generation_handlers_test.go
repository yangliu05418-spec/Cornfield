package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/provider"
)

func float64Pointer(value float64) *float64 { return &value }

func TestGenerationRequestHash(t *testing.T) {
	assetID := uuid.MustParse("ac2be23a-6f82-4c32-8165-9559cecf74fc")
	base := generationRequest{ModelID: "model", CapabilityRevision: "rev", Prompt: "prompt", AspectRatio: "1:1", Resolution: "1K", DrawCount: 1, InputAssetIDs: []uuid.UUID{assetID}}
	first, err := generationRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generationRequestHash(base)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("request hash is not stable SHA-256: %q / %q", first, second)
	}
	changed := base
	changed.DrawCount = 2
	changedHash, _ := generationRequestHash(changed)
	if changedHash == first {
		t.Fatal("different request body produced the same request hash")
	}
}

func TestPublicJobErrorNeverReturnsProviderDetail(t *testing.T) {
	for code, want := range map[string]string{
		"CONTENT_POLICY_REJECTED": "图片可能触发安全策略，请调整描述",
		"PROVIDER_HTTP_400":       "当前参数无法生成，请调整后重试",
		"SUBMISSION_INTERRUPTED":  "任务提交结果不确定，请等待核查或移除记录",
		"UNKNOWN_PROVIDER_ERROR":  "生成失败，请稍后重试",
	} {
		got := publicJobError(&code)
		if got == nil || *got != want {
			t.Errorf("publicJobError(%q) = %v, want %q", code, got, want)
		}
	}
}

func TestDismissibleJobStatus(t *testing.T) {
	for _, status := range []string{"failed", "submission_uncertain", "cancelled"} {
		if !dismissibleJobStatus(status) {
			t.Errorf("%s should be dismissible", status)
		}
	}
	for _, status := range []string{"queued", "submitting", "ingesting", "succeeded"} {
		if dismissibleJobStatus(status) {
			t.Errorf("%s should not be dismissible", status)
		}
	}
}

func TestDuplicateReferenceImagesAreRejected(t *testing.T) {
	first := uuid.MustParse("ac2be23a-6f82-4c32-8165-9559cecf74fc")
	second := uuid.MustParse("8dd52d1b-20b7-47e0-8406-430f604772cd")
	if !hasDuplicateAssetIDs([]uuid.UUID{first, second, first}) {
		t.Fatal("duplicate reference image was not detected")
	}
	if hasDuplicateAssetIDs([]uuid.UUID{first, second}) {
		t.Fatal("distinct reference images were treated as duplicates")
	}
}

func TestReferenceByteLimitFailsClosed(t *testing.T) {
	const limit = int64(10 << 20)
	if referenceExceedsModelLimit(limit, limit) {
		t.Fatal("reference at the model byte limit was rejected")
	}
	if !referenceExceedsModelLimit(limit+1, limit) {
		t.Fatal("reference above the model byte limit was accepted")
	}
	if !referenceExceedsModelLimit(1, 0) {
		t.Fatal("missing model byte limit did not fail closed")
	}
}

func TestGenerationTokenRefill(t *testing.T) {
	updatedAt := time.Unix(1_700_000_000, 0)
	if got := refillGenerationTokens(0, updatedAt, updatedAt.Add(5*time.Second)); got != 1 {
		t.Fatalf("refillGenerationTokens() = %v, want 1", got)
	}
	if got := refillGenerationTokens(3.5, updatedAt, updatedAt.Add(time.Minute)); got != generationBurstCapacity {
		t.Fatalf("refill exceeded burst capacity: %v", got)
	}
}

func TestControlledLegnextFlags(t *testing.T) {
	blocked := []string{"portrait --ar 16:9", "portrait\n--V=7", "portrait (--FAST)", "--repeat 4", "--r 3", "--stylize 200", "--future-flag=value", "https://example.com/reference.png portrait", "portrait <HTTP://example.com/a.jpg>"}
	for _, prompt := range blocked {
		if !containsControlledLegnextInput(prompt) {
			t.Fatalf("expected prompt to be blocked: %q", prompt)
		}
	}
	allowed := []string{"art--v deco", "a fast camera", "ar 16:9"}
	for _, prompt := range allowed {
		if containsControlledLegnextInput(prompt) {
			t.Fatalf("expected prompt to be allowed: %q", prompt)
		}
	}
}

func TestNormalizeMidjourneyOptions(t *testing.T) {
	input := generationRequest{DrawCount: 1, Options: provider.GenerationOptions{Midjourney: &provider.MidjourneyOptions{
		Version: "8.1", Resolution: "hd", Speed: "fast", Stylize: 100,
	}}}
	if err := normalizeGenerationOptions("legnext-midjourney", "legnext", []string{"8.1", "7"}, nil, 0, &input); err != nil {
		t.Fatal(err)
	}
	if input.Resolution != "HD" {
		t.Fatalf("resolution = %q", input.Resolution)
	}

	quality := 4.0
	input = generationRequest{DrawCount: 1, Options: provider.GenerationOptions{Midjourney: &provider.MidjourneyOptions{
		Version: "7", Speed: "turbo", Quality: &quality, Draft: true,
	}}}
	if err := normalizeGenerationOptions("legnext-midjourney", "legnext", []string{"8.1", "7"}, nil, 0, &input); err != nil {
		t.Fatal(err)
	}
	if input.Options.Midjourney.Quality != nil || input.Resolution != "auto" {
		t.Fatalf("V7 draft was not normalized: %+v", input)
	}

	input = generationRequest{DrawCount: 4, Options: provider.GenerationOptions{Midjourney: &provider.MidjourneyOptions{Version: "8.1", Speed: "fast"}}}
	if err := normalizeGenerationOptions("legnext-midjourney", "legnext", []string{"8.1", "7"}, nil, 0, &input); err == nil {
		t.Fatal("four Midjourney draws were accepted")
	}
}

func TestNormalizeEverySupportedMidjourneyVersion(t *testing.T) {
	versions := []string{"8.1", "8", "7", "6.1", "6", "niji 6"}
	tests := []struct {
		version    string
		resolution string
		quality    *float64
		wantRes    string
	}{
		{version: "8.1", resolution: "sd", wantRes: "SD"},
		{version: "8", resolution: "hd", quality: float64Pointer(4), wantRes: "HD"},
		{version: "7", quality: float64Pointer(2), wantRes: "auto"},
		{version: "6.1", quality: float64Pointer(0.5), wantRes: "auto"},
		{version: "6", quality: float64Pointer(1), wantRes: "auto"},
		{version: "niji 6", quality: float64Pointer(2), wantRes: "auto"},
	}
	for _, test := range tests {
		t.Run(test.version, func(t *testing.T) {
			input := generationRequest{DrawCount: 1, Options: provider.GenerationOptions{Midjourney: &provider.MidjourneyOptions{
				Version: test.version, Resolution: test.resolution, Speed: "fast", Quality: test.quality, Stylize: 100,
			}}}
			if err := normalizeGenerationOptions("legnext-midjourney", "legnext", versions, nil, 0, &input); err != nil {
				t.Fatal(err)
			}
			if input.Resolution != test.wantRes {
				t.Fatalf("resolution = %q, want %q", input.Resolution, test.wantRes)
			}
		})
	}

	invalid := []provider.MidjourneyOptions{
		{Version: "8.1", Speed: "fast", Quality: float64Pointer(1)},
		{Version: "8", Speed: "turbo", Quality: float64Pointer(1)},
		{Version: "7", Speed: "fast", Quality: float64Pointer(0.5)},
		{Version: "6.1", Speed: "fast", Quality: float64Pointer(4)},
		{Version: "niji 6", Speed: "fast", Draft: true},
	}
	for _, options := range invalid {
		input := generationRequest{DrawCount: 1, Options: provider.GenerationOptions{Midjourney: &options}}
		if err := normalizeGenerationOptions("legnext-midjourney", "legnext", versions, nil, 0, &input); err == nil {
			t.Fatalf("invalid options were accepted: %+v", options)
		}
	}
}

func TestUnsupportedOpenRouterControlsNormalizeToAuto(t *testing.T) {
	input := generationRequest{DrawCount: 1}
	if err := normalizeGenerationOptions("openrouter-test", "openrouter", nil, nil, 0, &input); err != nil {
		t.Fatal(err)
	}
	if input.AspectRatio != "auto" || input.Resolution != "auto" {
		t.Fatalf("input = %+v", input)
	}
}

func TestNormalizeImageQuality(t *testing.T) {
	input := generationRequest{DrawCount: 1}
	qualities := []string{"auto", "low", "medium", "high"}
	if err := normalizeGenerationOptions("openrouter-gpt-image-2", "openrouter", nil, qualities, 0, &input); err != nil {
		t.Fatal(err)
	}
	if input.Options.Image == nil || input.Options.Image.Quality != "auto" {
		t.Fatalf("quality was not defaulted: %+v", input.Options)
	}
	input.Options.Image.Quality = "ultra"
	if err := normalizeGenerationOptions("openrouter-gpt-image-2", "openrouter", nil, qualities, 0, &input); err == nil {
		t.Fatal("unsupported quality was accepted")
	}
}

func TestDesiredHTTPBatchStatus(t *testing.T) {
	if got := desiredBatchStatus(batchStateCounts{total: 4, succeeded: 2, failed: 1, cancelled: 1}); got != "partial" {
		t.Fatalf("desiredBatchStatus() = %q, want partial", got)
	}
	if got := desiredBatchStatus(batchStateCounts{total: 2, cancelled: 2}); got != "cancelled" {
		t.Fatalf("desiredBatchStatus() = %q, want cancelled", got)
	}
	if got := batchStatusWithOutputCount("succeeded", 2, 4); got != "partial" {
		t.Fatalf("batchStatusWithOutputCount() = %q, want partial", got)
	}
}

func TestGenerationCursorAndPage(t *testing.T) {
	firstID := uuid.MustParse("211e8b9f-caec-419e-8f21-0cf1d2af92dd")
	secondID := uuid.MustParse("a9ce273e-62d0-4b8a-b3d4-0f26fcc1c090")
	thirdID := uuid.MustParse("f84ccb4d-1111-4cc9-a46b-f57b04fa6b06")
	now := time.Date(2026, time.July, 17, 12, 0, 0, 123, time.UTC)
	items := []batchResponse{
		{ID: firstID, CreatedAt: now},
		{ID: secondID, CreatedAt: now.Add(-time.Second)},
		{ID: thirdID, CreatedAt: now.Add(-2 * time.Second)},
	}

	page, nextCursor := finishGenerationPage(items, 2)
	if len(page) != 2 || page[0].ID != firstID || page[1].ID != secondID || nextCursor == "" {
		t.Fatalf("page = %+v, next_cursor = %q", page, nextCursor)
	}
	cursorTime, cursorID, err := decodeGenerationCursor(nextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if cursorID != secondID || !cursorTime.Equal(items[1].CreatedAt) {
		t.Fatalf("cursor = %s %s", cursorID, cursorTime)
	}
	if _, next := finishGenerationPage(page, 2); next != "" {
		t.Fatalf("unexpected cursor for final page: %q", next)
	}
	if _, _, err := decodeGenerationCursor("not-base64"); err == nil {
		t.Fatal("expected invalid cursor error")
	}
	if _, _, err := decodeGenerationCursor(strings.Repeat("a", 513)); err == nil {
		t.Fatal("expected oversized cursor error")
	}
}

func TestGenerationAssemblerPreservesOrderAndEmptyArrays(t *testing.T) {
	batchOne := uuid.MustParse("e895cb70-d5fb-4705-9549-95df252e4669")
	batchTwo := uuid.MustParse("3aa4c6df-10de-45ff-b738-e0a65c433e89")
	jobOne := uuid.MustParse("f7395755-4b81-4d13-b557-a5c569d7f70e")
	jobTwo := uuid.MustParse("65c241a8-baca-449e-acdd-a4f599bef6f3")
	assetID := uuid.MustParse("64acdde7-6803-41a2-b185-818de4f854d6")

	assembler := newGenerationAssembler([]batchResponse{
		{ID: batchOne, DrawCount: 2},
		{ID: batchTwo, DrawCount: 1},
	})
	if !assembler.addJob(batchOne, jobResponse{ID: jobOne, DrawIndex: 0, ExpectedOutputs: 2}) ||
		!assembler.addJob(batchOne, jobResponse{ID: jobTwo, DrawIndex: 1, ExpectedOutputs: 1}) {
		t.Fatal("known batch was not assembled")
	}
	output := generationOutputResponse{AssetID: assetID, OutputIndex: 0}
	setGenerationOutputURLs(&output)
	if !assembler.addOutput(jobOne, output) {
		t.Fatal("known job output was not assembled")
	}
	if assembler.addOutput(uuid.New(), output) {
		t.Fatal("unknown job output was accepted")
	}
	if got := assembler.items[0].Jobs; len(got) != 2 || got[0].ID != jobOne || got[1].ID != jobTwo || len(got[0].Outputs) != 1 {
		t.Fatalf("jobs = %+v", got)
	}
	if len(assembler.items[1].Jobs) != 0 || assembler.items[0].Jobs[1].Outputs == nil {
		t.Fatalf("empty arrays were not initialized: %+v", assembler.items)
	}
	encoded, err := json.Marshal(assembler.items)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	if !strings.Contains(jsonText, `"jobs":[]`) || !strings.Contains(jsonText, `"outputs":[]`) {
		t.Fatalf("empty arrays encoded as null: %s", jsonText)
	}
}
