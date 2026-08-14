package httpapi

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateRasterMaskChangesAcceptsEdgeTilesAndDeletes(t *testing.T) {
	changes := []rasterMaskChange{
		{TileX: 0, TileY: 0, Width: 256, Height: 256, Action: "put", Part: "tile-0-0"},
		{TileX: 1, TileY: 1, Width: 44, Height: 44, Action: "delete"},
	}
	if err := validateRasterMaskChanges(changes, 300, 300); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRasterMaskChangesRejectsAmbiguousOrInvalidTiles(t *testing.T) {
	valid := rasterMaskChange{TileX: 0, TileY: 0, Width: 256, Height: 256, Action: "put", Part: "tile"}
	tests := map[string][]rasterMaskChange{
		"empty":          nil,
		"duplicate tile": {valid, valid},
		"duplicate part": {valid, {TileX: 1, TileY: 0, Width: 44, Height: 256, Action: "put", Part: "tile"}},
		"wrong edge":     {{TileX: 1, TileY: 0, Width: 256, Height: 256, Action: "delete"}},
		"outside":        {{TileX: 2, TileY: 0, Width: 1, Height: 256, Action: "delete"}},
		"unknown action": {{TileX: 0, TileY: 0, Width: 256, Height: 256, Action: "replace"}},
	}
	for name, changes := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateRasterMaskChanges(changes, 300, 300); err == nil {
				t.Fatal("invalid changes were accepted")
			}
		})
	}
}

func TestRasterMaskStorageKeyIsContentAddressedAndSafe(t *testing.T) {
	digest := strings.Repeat("a", 64)
	if got, want := rasterMaskStorageKey(digest), "aa/aa/"+digest+"/original.a8"; got != want {
		t.Fatalf("storage key = %q, want %q", got, want)
	}
}

func TestReadRasterMaskManifestRejectsTrailingJSON(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormField("manifest")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(`{"expected_project_revision":1,"expected_mask_version":0,"changes":[]} {}`))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	if _, _, err = readRasterMaskManifest(response, request); err == nil {
		t.Fatal("manifest with trailing JSON was accepted")
	}
}
