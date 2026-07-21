package worker

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/config"
	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
)

func TestUserFacingGenerationError(t *testing.T) {
	for code, want := range map[string]string{
		"CONTENT_POLICY_REJECTED": "图片可能触发安全策略，请调整描述",
		"PROVIDER_HTTP_422":       "当前参数无法生成，请调整后重试",
		"PROVIDER_IMAGE_INVALID":  "生成结果无法处理，请调整参数后重试",
		"PROVIDER_HTTP_429":       "生成服务繁忙，请稍后重试",
		"UNRECOGNIZED":            "生成失败，请稍后重试",
	} {
		if got := userFacingGenerationError(code); got != want {
			t.Errorf("userFacingGenerationError(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestGenerationOutputURLAllowed(t *testing.T) {
	allowed, _ := url.Parse("https://delivery.us.bfl.ai/result.png")
	disallowed, _ := url.Parse("https://delivery.us.bfl.ai.evil.example/result.png")
	insecure, _ := url.Parse("http://delivery.us.bfl.ai/result.png")
	patterns := []string{"delivery.*.bfl.ai"}
	if !generationOutputURLAllowed(allowed, patterns) {
		t.Fatal("expected allowlisted HTTPS output URL")
	}
	if generationOutputURLAllowed(disallowed, patterns) || generationOutputURLAllowed(insecure, patterns) {
		t.Fatal("unsafe output URL was allowed")
	}
}

func TestOptionalThumbnailQueueIsBoundedAndNonBlocking(t *testing.T) {
	queue := make(chan string, 1)
	worker := &GenerateWorker{OptionalThumbQueue: queue}
	worker.queueOptionalThumbnail("first")
	worker.queueOptionalThumbnail("dropped")
	if got := <-queue; got != "first" {
		t.Fatalf("queued key = %q", got)
	}
}

func TestPrepareGenerationOutputsDownloadsConcurrentlyAndKeepsOrder(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	pngBytes := testPNG(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		time.Sleep(40 * time.Millisecond)
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write(pngBytes)
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "uploads", "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	worker := &GenerateWorker{Config: config.Config{AssetRoot: root}, HTTPClient: server.Client()}
	item := generationRecord{
		JobID: uuid.New(),
		ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{
			AllowedOutputHosts: []string{"127.0.0.1"},
		}},
	}
	images := make([]provider.Image, 4)
	for index := range images {
		images[index].URL = server.URL + "/" + string(rune('a'+index)) + ".png"
	}
	prepared, err := worker.prepareGenerationOutputs(context.Background(), item, images)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, output := range prepared {
			_ = os.Remove(output.tempPath)
		}
	}()
	if maximum.Load() < 2 {
		t.Fatalf("downloads were not concurrent; maximum active requests = %d", maximum.Load())
	}
	for index, output := range prepared {
		if output.output.OutputIndex != index {
			t.Fatalf("output at position %d has index %d", index, output.output.OutputIndex)
		}
		if _, err := os.Stat(output.tempPath); err != nil {
			t.Fatalf("prepared output %d missing: %v", index, err)
		}
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 120, G: 180, B: 80, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
