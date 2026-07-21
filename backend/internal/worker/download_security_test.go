package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"internal-image-studio/internal/modelconfig"
)

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failed and included token=transport-secret")
}

type scriptedDownloadTransport struct {
	statuses []int
	calls    int
}

func (transport *scriptedDownloadTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status := transport.statuses[min(transport.calls, len(transport.statuses)-1)]
	transport.calls++
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Retry-After": []string{"0"}},
		Body:       io.NopCloser(strings.NewReader("image-bytes")),
		Request:    request,
	}, nil
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

func TestDownloadRetriesOnlyTransientResponses(t *testing.T) {
	transport := &scriptedDownloadTransport{statuses: []int{http.StatusBadGateway, http.StatusTooManyRequests, http.StatusOK}}
	worker := &GenerateWorker{HTTPClient: &http.Client{Transport: transport}}
	item := generationRecord{ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{AllowedOutputHosts: []string{"cdn.legnext.ai"}}}}
	target := filepath.Join(t.TempDir(), "output.part")
	if err := worker.downloadToFile(context.Background(), item, "https://cdn.legnext.ai/output.png", target); err != nil {
		t.Fatalf("downloadToFile: %v", err)
	}
	if transport.calls != 3 {
		t.Fatalf("calls = %d, want 3", transport.calls)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != "image-bytes" {
		t.Fatalf("downloaded body = %q, err = %v", body, err)
	}

	transport = &scriptedDownloadTransport{statuses: []int{http.StatusBadRequest, http.StatusOK}}
	worker.HTTPClient = &http.Client{Transport: transport}
	err := worker.downloadToFile(context.Background(), item, "https://cdn.legnext.ai/output.png", filepath.Join(t.TempDir(), "rejected.part"))
	if err == nil || transport.calls != 1 {
		t.Fatalf("non-retryable result: err=%v calls=%d", err, transport.calls)
	}
}
