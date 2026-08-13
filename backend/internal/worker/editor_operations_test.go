package worker

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	studioEditor "internal-image-studio/internal/editor"
	"internal-image-studio/internal/modelconfig"
)

func TestAssetOperationTimeoutsComeFromCatalog(t *testing.T) {
	worker := &AssetOperationWorker{Catalog: &modelconfig.Catalog{Models: []modelconfig.Model{{
		ID:      "byteplus-test",
		Enabled: true,
		Policy: modelconfig.Policy{
			SubmitTimeoutSeconds:             300,
			LayerDecompositionTimeoutSeconds: 600,
		},
	}}}}
	modelID := "byteplus-test"
	got, err := worker.layerOperationTimeout(assetOperationRecord{ModelID: &modelID})
	if err != nil || got != 600*time.Second {
		t.Fatalf("layer timeout = %v, %v", got, err)
	}
	if got = worker.operationRecoveryAge(); got != 630*time.Second {
		t.Fatalf("recovery age = %v", got)
	}
	worker.Catalog = nil
	if got = worker.operationRecoveryAge(); got != 0 {
		t.Fatalf("missing catalog recovery age = %v", got)
	}
}

func TestBytePlusLayerURLAllowlist(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{raw: "https://ark-doc.tos-ap-southeast-1.bytepluses.com/image.png?sig=private", want: true},
		{raw: "https://cdn.byteplus.com/image.png", want: true},
		{raw: "https://ark-acg-ap-southeast-1.tos-ap-southeast-1.volces.com/image.png?sig=private", want: true},
		{raw: "https://tos-ap-southeast-1.volces.com/image.png", want: true},
		{raw: "https://tos-ap-southeast-1.volces.com.evil.example/image.png", want: false},
		{raw: "https://tos-us-east-1.volces.com/image.png", want: false},
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

func TestCompositeEditorDocumentMatchesCSSAffineSemantics(t *testing.T) {
	redID, blueID := uuid.New(), uuid.New()
	red := solidNRGBA(4, 4, color.NRGBA{R: 255, A: 255})
	blue := solidNRGBA(2, 2, color.NRGBA{B: 255, A: 255})
	angle := math.Pi / 2
	document := studioEditor.Document{
		SchemaVersion: 1,
		Canvas:        studioEditor.Canvas{Width: 8, Height: 8},
		Objects: []studioEditor.Object{
			{ID: "red", AssetID: redID, Transform: [6]float64{1, 0, 0, 1, 1, 1}, Opacity: 1, Visible: true, ZIndex: 0,
				Crop: &studioEditor.Crop{X: 0, Y: 0, Width: 0.5, Height: 1}},
			{ID: "blue", AssetID: blueID, Transform: [6]float64{math.Cos(angle), math.Sin(angle), -math.Sin(angle), math.Cos(angle), 5, 2}, Opacity: 0.5, Visible: true, ZIndex: 1},
		},
	}
	canvas, err := compositeEditorDocument(context.Background(), document, func(id uuid.UUID) (image.Image, error) {
		switch id {
		case redID:
			return red, nil
		case blueID:
			return blue, nil
		default:
			t.Fatalf("unexpected asset %s", id)
			return nil, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, a := canvas.At(1, 1).RGBA(); r < 0xf000 || a != 0xffff {
		t.Fatalf("cropped red pixel = %#v", canvas.At(1, 1))
	}
	if _, _, _, a := canvas.At(3, 1).RGBA(); a != 0 {
		t.Fatalf("cropped-away pixel remains visible: %#v", canvas.At(3, 1))
	}
	if _, _, b, a := canvas.At(4, 2).RGBA(); b < 0x7000 || b > 0x9000 || a < 0x7000 || a > 0x9000 {
		t.Fatalf("rotated translucent blue pixel = %#v", canvas.At(4, 2))
	}
}

func TestCompositeEditorDocumentSupportsFlipsAndLayerOrder(t *testing.T) {
	bottomID, topID := uuid.New(), uuid.New()
	bottom := solidNRGBA(3, 1, color.NRGBA{G: 255, A: 255})
	top := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	top.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	top.SetNRGBA(1, 0, color.NRGBA{B: 255, A: 255})
	document := studioEditor.Document{
		SchemaVersion: 1,
		Canvas:        studioEditor.Canvas{Width: 4, Height: 2},
		Objects: []studioEditor.Object{
			{ID: "top", AssetID: topID, Transform: [6]float64{-1, 0, 0, 1, 3, 0}, Opacity: 1, Visible: true, ZIndex: 1},
			{ID: "bottom", AssetID: bottomID, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: true, ZIndex: 0},
		},
	}
	canvas, err := compositeEditorDocument(context.Background(), document, func(id uuid.UUID) (image.Image, error) {
		if id == topID {
			return top, nil
		}
		return bottom, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if r, g, _, _ := canvas.At(2, 0).RGBA(); r < 0xf000 || g > 0x1000 {
		t.Fatalf("flipped top layer did not cover bottom: %#v", canvas.At(2, 0))
	}
}

func TestCompositeEditorSceneAppliesIndependentAlphaMask(t *testing.T) {
	contentID, maskID := uuid.New(), uuid.New()
	content := solidNRGBA(4, 4, color.NRGBA{R: 255, A: 255})
	mask := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			alpha := uint8(0)
			if x >= 2 {
				alpha = 255
			}
			mask.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: alpha})
		}
	}
	maskNodeID := "mask"
	scene := studioEditor.RenderScene{
		Canvas: studioEditor.Canvas{Width: 8, Height: 8},
		Nodes: []studioEditor.RenderNode{
			{ID: maskNodeID, AssetID: maskID, Transform: [6]float64{1, 0, 0, 1, 1, 1}, Opacity: .5, Visible: true, Order: 0, Role: studioEditor.RenderRoleMask},
			{ID: "content", AssetID: contentID, Transform: [6]float64{1, 0, 0, 1, 1, 1}, Opacity: 1, Visible: true, Order: 1, Role: studioEditor.RenderRoleContent, MaskNodeID: &maskNodeID},
		},
	}
	canvas, err := compositeEditorScene(context.Background(), scene, func(id uuid.UUID) (image.Image, error) {
		if id == maskID {
			return mask, nil
		}
		return content, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, alpha := canvas.At(1, 2).RGBA(); alpha != 0 {
		t.Fatalf("masked-out pixel remains visible: %#v", canvas.At(1, 2))
	}
	if red, _, _, alpha := canvas.At(3, 2).RGBA(); red < 0x7000 || red > 0x9000 || alpha < 0x7000 || alpha > 0x9000 {
		t.Fatalf("mask opacity was not applied: %#v", canvas.At(3, 2))
	}
}

func TestCompositeEditorSceneDoesNotRevealContentWhenMaskIsMissingOrHidden(t *testing.T) {
	contentID, maskAssetID := uuid.New(), uuid.New()
	maskNodeID := "mask"
	base := studioEditor.RenderScene{
		Canvas: studioEditor.Canvas{Width: 4, Height: 4},
		Nodes: []studioEditor.RenderNode{
			{ID: maskNodeID, AssetID: maskAssetID, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: false, Order: 0, Role: studioEditor.RenderRoleMask},
			{ID: "content", AssetID: contentID, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: true, Order: 1, Role: studioEditor.RenderRoleContent, MaskNodeID: &maskNodeID},
		},
	}
	for _, test := range []struct {
		name  string
		scene studioEditor.RenderScene
	}{
		{name: "hidden", scene: base},
		{name: "missing", scene: studioEditor.RenderScene{Canvas: base.Canvas, Nodes: base.Nodes[1:]}},
	} {
		t.Run(test.name, func(t *testing.T) {
			canvas, err := compositeEditorScene(context.Background(), test.scene, func(uuid.UUID) (image.Image, error) {
				return solidNRGBA(4, 4, color.NRGBA{R: 255, A: 255}), nil
			})
			if test.name == "missing" {
				if err == nil {
					t.Fatal("missing mask reference was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, _, alpha := canvas.At(1, 1).RGBA(); alpha != 0 {
				t.Fatalf("content leaked through hidden mask: %#v", canvas.At(1, 1))
			}
		})
	}
}

func solidNRGBA(width, height int, value color.NRGBA) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			result.SetNRGBA(x, y, value)
		}
	}
	return result
}
