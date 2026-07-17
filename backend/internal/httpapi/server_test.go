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
