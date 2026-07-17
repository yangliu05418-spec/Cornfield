package worker

import (
	"context"
	"errors"
	"net/http"
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
	_, _, err := worker.download(context.Background(), item, "https://cdn.legnext.ai/output.png?token=url-secret")
	if err == nil {
		t.Fatal("download unexpectedly succeeded")
	}
	for _, secret := range []string{"url-secret", "transport-secret", "token="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("download error exposed secret marker %q: %v", secret, err)
		}
	}
}
