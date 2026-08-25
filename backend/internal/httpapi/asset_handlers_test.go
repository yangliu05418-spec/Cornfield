package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAssetCursorRoundTrip(t *testing.T) {
	wantTime := time.Date(2026, 7, 17, 10, 30, 0, 123, time.UTC)
	wantID := uuid.MustParse("e716a8a2-c1db-4881-9bd0-7aaa7208b55a")

	encoded := encodeAssetCursor(wantTime, wantID)
	gotTime, gotID, err := decodeAssetCursor(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !gotTime.Equal(wantTime) || gotID != wantID {
		t.Fatalf("cursor mismatch: got %s %s", gotTime, gotID)
	}
}

func TestUploadFilenameBounds(t *testing.T) {
	for _, value := range []string{"reference.png", "参考图.webp", strings.Repeat("a", 251) + ".png"} {
		if !validUploadFilename(value) {
			t.Fatalf("valid filename %q was rejected", value)
		}
	}
	for _, value := range []string{"", "bad\tname.png", strings.Repeat("a", 256)} {
		if validUploadFilename(value) {
			t.Fatalf("invalid filename %q was accepted", value)
		}
	}
}

func TestNormalizeDeclaredUploadMediaType(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "image/jpeg", want: "image/jpeg", ok: true},
		{input: "IMAGE/JPG", want: "image/jpeg", ok: true},
		{input: "image/pjpeg", want: "image/jpeg", ok: true},
		{input: "image/x-png", want: "image/png", ok: true},
		{input: "image/webp; charset=binary", want: "image/webp", ok: true},
		{input: "", want: "application/octet-stream", ok: true},
		{input: "application/octet-stream", want: "application/octet-stream", ok: true},
		{input: "image/gif", ok: false},
		{input: "image/svg+xml", ok: false},
	}
	for _, test := range tests {
		got, ok := normalizeDeclaredUploadMediaType(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("normalizeDeclaredUploadMediaType(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestNormalizeUploadPurpose(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "", want: "library", ok: true},
		{input: " library ", want: "library", ok: true},
		{input: "REFERENCE", want: "reference", ok: true},
		{input: "hidden", ok: false},
	} {
		got, ok := normalizeUploadPurpose(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("normalizeUploadPurpose(%q) = %q, %v; want %q, %v", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestAssetDownloadFilenameUsesActualMediaType(t *testing.T) {
	jpegName := "download.png"
	pathName := `C:\Users\person\photo.jpeg`
	tests := []struct {
		name      *string
		mediaType string
		want      string
	}{
		{name: &jpegName, mediaType: "image/jpeg", want: "download.jpg"},
		{name: &pathName, mediaType: "image/png", want: "photo.png"},
		{name: nil, mediaType: "image/webp", want: "image.webp"},
	}
	for _, test := range tests {
		if got := assetDownloadFilename(test.name, test.mediaType); got != test.want {
			t.Errorf("assetDownloadFilename(%v, %q) = %q; want %q", test.name, test.mediaType, got, test.want)
		}
	}
}

func TestAssetCursorRejectsInvalidInput(t *testing.T) {
	if _, _, err := decodeAssetCursor("not-base64"); err == nil {
		t.Fatal("expected invalid cursor error")
	}
}

func TestUniqueAssetIDs(t *testing.T) {
	first, second := uuid.New(), uuid.New()
	got, ok := uniqueAssetIDs([]uuid.UUID{first, second, first}, 500)
	if !ok || len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("uniqueAssetIDs() = %v, %v", got, ok)
	}
	tests := []struct {
		values  []uuid.UUID
		maximum int
	}{
		{values: nil, maximum: 500},
		{values: []uuid.UUID{uuid.Nil}, maximum: 500},
		{values: []uuid.UUID{first, second}, maximum: 1},
	}
	for _, test := range tests {
		if _, valid := uniqueAssetIDs(test.values, test.maximum); valid {
			t.Fatalf("invalid values accepted: %v", test.values)
		}
	}
}
