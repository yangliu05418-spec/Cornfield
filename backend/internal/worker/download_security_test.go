package worker

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"internal-image-studio/internal/modelconfig"
)

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed and included token=transport-secret")
}

func TestDownloadErrorDoesNotExposeSignedProviderURL(t *testing.T) {
	worker := &GenerateWorker{HTTPClient: &http.Client{Transport: failingRoundTripper{}}}
	item := generationRecord{ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{
		AllowedOutputHosts: []string{"cdn.legnext.ai"},
	}}}
	err := worker.downloadToFile(context.Background(), item, "https://cdn.legnext.ai/output.png?token=url-secret", filepath.Join(t.TempDir(), "output.part"))
	if err == nil {
		t.Fatal("download unexpectedly succeeded")
	}
	for _, secret := range []string{"url-secret", "transport-secret", "token="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("download error exposed secret marker %q: %v", secret, err)
		}
	}
}

func TestBFLOutputHostPatternIsNarrow(t *testing.T) {
	for _, host := range []string{"delivery.eu1.bfl.ai", "delivery.us2.bfl.ai"} {
		if !outputHostAllowed(host, "delivery.*.bfl.ai") {
			t.Fatalf("expected %q to be allowed", host)
		}
	}
	for _, host := range []string{"api.bfl.ai", "delivery.bfl.ai", "delivery.a.b.bfl.ai", "delivery.eu1.bfl.ai.example.com"} {
		if outputHostAllowed(host, "delivery.*.bfl.ai") {
			t.Fatalf("expected %q to be rejected", host)
		}
	}
}
