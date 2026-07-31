package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidDirectorProjectName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want bool
	}{
		{"导演台 01", true},
		{"", false},
		{strings.Repeat("界", 64), true},
		{strings.Repeat("界", 65), false},
	} {
		if got := validDirectorProjectName(test.name); got != test.want {
			t.Fatalf("validDirectorProjectName(%q)=%v, want %v", test.name, got, test.want)
		}
	}
}

func TestValidDirectorDocument(t *testing.T) {
	t.Parallel()
	if !validDirectorDocument(json.RawMessage(`{"format":"3d-director-desk-project","project":{}}`)) {
		t.Fatal("valid object was rejected")
	}
	for _, value := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`[]`), json.RawMessage(`{"broken"`)} {
		if validDirectorDocument(value) {
			t.Fatalf("invalid document was accepted: %q", value)
		}
	}
}
