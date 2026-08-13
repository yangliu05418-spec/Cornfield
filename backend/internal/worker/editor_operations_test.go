package worker

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	studioEditor "internal-image-studio/internal/editor"
)

func TestBytePlusLayerURLAllowlist(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://ark-doc.tos-ap-southeast-1.bytepluses.com/image.png?sig=private", want: true},
		{raw: "https://cdn.byteplus.com/image.png", want: true},
		{raw: "http://ark-doc.tos-ap-southeast-1.bytepluses.com/image.png", want: false},
		{raw: "https://bytepluses.com.evil.example/image.png", want: false},
		{raw: "https://127.0.0.1/image.png", want: false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := bytePlusLayerURLAllowed(parsed); got != test.want {
			t.Errorf("bytePlusLayerURLAllowed(%q) = %v, want %v", test.raw, got, test.want)
		}
	}
}

func TestEncodeOpaqueJPEGRequiresOpaqueImage(t *testing.T) {
	writePNG := func(name string, alpha uint8) string {
		path := filepath.Join(t.TempDir(), name)
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		imageValue := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		for y := 0; y < 4; y++ {
			for x := 0; x < 4; x++ {
				imageValue.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: alpha})
			}
		}
		if err = png.Encode(file, imageValue); err != nil {
			t.Fatal(err)
		}
		if err = file.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}

	opaque := writePNG("opaque.png", 255)
	if err := encodeOpaqueJPEG(opaque, 95); err != nil {
		t.Fatalf("opaque conversion failed: %v", err)
	}
	converted, err := os.Open(opaque)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.Decode(converted)
	_ = converted.Close()
	if err != nil || format != "jpeg" {
		t.Fatalf("converted format = %q, error = %v", format, err)
	}

	transparent := writePNG("transparent.png", 200)
	if err := encodeOpaqueJPEG(transparent, 95); err == nil {
		t.Fatal("transparent image was converted to JPEG")
	}
}

func TestSafeArchiveNameRemovesPaths(t *testing.T) {
	if got := safeArchiveName("../foreground\\subject"); got != "-foreground-subject" {
		t.Fatalf("safeArchiveName() = %q", got)
	}
}

func TestCompositeEditorDocumentAppliesLayerTransform(t *testing.T) {
	assetID := uuid.New()
	source := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	document := studioEditor.Document{
		SchemaVersion: 1,
		Canvas:        studioEditor.Canvas{Width: 4, Height: 4},
		Objects: []studioEditor.Object{{
			ID: "subject", AssetID: assetID, Transform: [6]float64{1, 0, 0, 1, 1, 1},
			Opacity: 1, Visible: true, ZIndex: 0,
		}},
	}
	canvas, err := compositeEditorDocument(context.Background(), document, func(id uuid.UUID) (image.Image, error) {
		if id != assetID {
			t.Fatalf("unexpected asset %s", id)
		}
		return source, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if red, _, _, alpha := canvas.At(1, 1).RGBA(); red != 0xffff || alpha != 0xffff {
		t.Fatalf("transformed pixel = %#v", canvas.At(1, 1))
	}
	if _, _, _, alpha := canvas.At(0, 0).RGBA(); alpha != 0 {
		t.Fatalf("outside pixel is not transparent: %#v", canvas.At(0, 0))
	}
}
