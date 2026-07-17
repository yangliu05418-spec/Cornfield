package provider

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxProviderRequestIDBytes = 256

func responseTelemetry(response *http.Response, bodyIDs ...string) Telemetry {
	return responseTelemetryExcluding(response, nil, bodyIDs...)
}

func responseTelemetryExcluding(response *http.Response, secrets []string, bodyIDs ...string) Telemetry {
	telemetry := Telemetry{}
	if response.StatusCode >= 100 && response.StatusCode <= 599 {
		telemetry.HTTPStatus = response.StatusCode
	}
	candidates := []string{
		response.Header.Get("X-Request-Id"),
		response.Header.Get("X-Generation-Id"),
		response.Header.Get("Request-Id"),
	}
	candidates = append(candidates, bodyIDs...)
	for _, candidate := range candidates {
		if requestID := normalizeProviderIdentifier(candidate, secrets...); requestID != "" {
			telemetry.ProviderRequestID = requestID
			break
		}
	}
	return telemetry
}

func normalizeProviderRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxProviderRequestIDBytes || !utf8.ValidString(value) {
		return ""
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return value
}

func normalizeProviderIdentifier(value string, secrets ...string) string {
	identifier := normalizeProviderRequestID(value)
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" && strings.Contains(identifier, secret) {
			return ""
		}
	}
	return identifier
}
