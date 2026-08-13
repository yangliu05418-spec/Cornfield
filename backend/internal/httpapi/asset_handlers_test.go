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
