package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestResponseTelemetryUsesSafeBoundedIdentifier(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header)}
	response.Header.Set("X-Request-Id", strings.Repeat("x", maxProviderRequestIDBytes+1))
	response.Header.Set("X-Generation-Id", "generation-1")
	telemetry := responseTelemetry(response, "body-id")
	if telemetry.ProviderRequestID != "generation-1" || telemetry.HTTPStatus != http.StatusAccepted {
		t.Fatalf("telemetry = %+v", telemetry)
	}
	response.Header.Set("X-Request-Id", "request\ninvalid")
	response.Header.Del("X-Generation-Id")
	telemetry = responseTelemetry(response, "body-id")
	if telemetry.ProviderRequestID != "body-id" {
		t.Fatalf("telemetry = %+v", telemetry)
	}
	response.StatusCode = 700
	response.Header.Set("X-Request-Id", "prefix-secret-value-suffix")
	telemetry = responseTelemetryExcluding(response, []string{"secret-value"}, "safe-body-id")
	if telemetry.ProviderRequestID != "safe-body-id" || telemetry.HTTPStatus != 0 {
		t.Fatalf("invalid/secret telemetry survived: %+v", telemetry)
	}
	if identifier := normalizeProviderIdentifier("prefix-secret-value-suffix", "secret-value"); identifier != "" {
		t.Fatalf("secret provider identifier survived: %q", identifier)
	}
	normalized := (Telemetry{ProviderRequestID: "bad\nrequest", HTTPStatus: 999}).Normalized()
	if normalized.ProviderRequestID != "" || normalized.HTTPStatus != 0 {
		t.Fatalf("invalid adapter telemetry survived normalization: %+v", normalized)
	}
}
