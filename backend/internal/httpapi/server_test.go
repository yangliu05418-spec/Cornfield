package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginClientIPUsesNginxRealIP(t *testing.T) {
	request := &http.Request{RemoteAddr: "127.0.0.1:4567", Header: make(http.Header)}
	request.Header.Set("X-Real-IP", "203.0.113.9")
	if got := loginClientIP(request); got != "203.0.113.9" {
		t.Fatalf("loginClientIP = %q", got)
	}
	request.Header.Set("X-Real-IP", "not-an-ip")
	if got := loginClientIP(request); got != "127.0.0.1" {
		t.Fatalf("invalid forwarded IP fallback = %q", got)
	}
}

func TestDecodeJSONRejectsTrailingValue(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"first"} {"name":"second"}`))
	response := httptest.NewRecorder()
	var payload struct {
		Name string `json:"name"`
	}
	if decodeJSON(response, request, &payload) {
		t.Fatal("decodeJSON accepted more than one JSON value")
	}
	if response.Code != 400 {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}

func TestDecodeJSONAcceptsTrailingWhitespace(t *testing.T) {
	request := httptest.NewRequest("POST", "/", strings.NewReader("{\"name\":\"first\"}\n\t"))
	response := httptest.NewRecorder()
	var payload struct {
		Name string `json:"name"`
	}
	if !decodeJSON(response, request, &payload) {
		t.Fatalf("decodeJSON rejected valid payload: %s", response.Body.String())
	}
	if payload.Name != "first" {
		t.Fatalf("name = %q", payload.Name)
	}
}

func TestWriteAPIReadinessMetric(t *testing.T) {
	for _, test := range []struct {
		name  string
		ready bool
		value string
	}{
		{name: "ready", ready: true, value: "1"},
		{name: "not ready", ready: false, value: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			writeAPIReadinessMetric(&output, test.ready)
			want := "# TYPE image_studio_api_ready gauge\nimage_studio_api_ready " + test.value + "\n"
			if output.String() != want {
				t.Fatalf("metric output = %q, want %q", output.String(), want)
			}
		})
	}
}
