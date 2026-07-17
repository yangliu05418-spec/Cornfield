package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

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
