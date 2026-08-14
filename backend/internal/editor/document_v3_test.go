package editor

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDocumentV3MigratesV2WithoutChangingRenderablePixels(t *testing.T) {
	v1 := New(uuid.New(), 640, 480)
	v1.Objects[0].Transform = [6]float64{.75, 0, 0, .75, 12, 34}
	v2, err := MigrateV1ToV2(v1)
	if err != nil {
		t.Fatal(err)
	}
	v3, err := MigrateV2ToV3(v2)
	if err != nil {
		t.Fatal(err)
	}
	if v3.ActiveArtboardID != "artboard-1" || len(v3.Artboards) != 1 {
		t.Fatalf("unexpected migration: %#v", v3)
	}
	before, err := CompileV2RenderScene(v2)
	if err != nil {
		t.Fatal(err)
	}
	after, err := CompileV3ArtboardRenderScene(v3, v3.ActiveArtboardID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Canvas != after.Canvas || len(before.Nodes) != len(after.Nodes) || before.Nodes[0].Transform != after.Nodes[0].Transform {
		t.Fatalf("rendering changed: before=%#v after=%#v", before, after)
	}
	raw, err := json.Marshal(v3)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAny(raw)
	if err != nil || decoded.SchemaVersion != 3 || decoded.V3 == nil {
		t.Fatalf("DecodeAny(V3) = %#v, %v", decoded, err)
	}
}

func TestDocumentV3AllowsEmptyArtboardsAndRejectsUnsafeBounds(t *testing.T) {
	document := DocumentV3{
		SchemaVersion: 3, RendererSemanticsVersion: 2, ActiveArtboardID: "blank",
		Artboards: []ArtboardV3{{ID: "blank", Name: "空白画板", OrderKey: "00000000", Width: 1024, Height: 1024, Visible: true, Nodes: []NodeV2{}}},
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("blank artboard rejected: %v", err)
	}
	document.Artboards[0].X = MaxWorkspaceCoordinateV3 + 1
	if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("unsafe coordinate accepted: %v", err)
	}
}

func TestCompileV3CompositePreservesWorkspacePlacementAndOrder(t *testing.T) {
	firstAsset, secondAsset := uuid.New(), uuid.New()
	document := DocumentV3{
		SchemaVersion: 3, RendererSemanticsVersion: 2, ActiveArtboardID: "left",
		Artboards: []ArtboardV3{
			{ID: "left", Name: "左", OrderKey: "00000000", X: -20, Y: 10, Width: 100, Height: 80, Visible: true, Nodes: []NodeV2{rasterNodeV3("left-layer", firstAsset)}},
			{ID: "right", Name: "右", OrderKey: "00000001", X: 140, Y: 30, Width: 60, Height: 40, Visible: true, Nodes: []NodeV2{rasterNodeV3("right-layer", secondAsset)}},
		},
	}
	scene, err := CompileV3CompositeRenderScene(document, []string{"right", "left"})
	if err != nil {
		t.Fatal(err)
	}
	if scene.Canvas != (Canvas{Width: 220, Height: 80}) {
		t.Fatalf("unexpected composite canvas: %#v", scene.Canvas)
	}
	if len(scene.Nodes) != 2 || scene.Nodes[0].ID != "left-layer" || scene.Nodes[1].ID != "right-layer" {
		t.Fatalf("unexpected order: %#v", scene.Nodes)
	}
	if scene.Nodes[0].Transform[4] != 0 || scene.Nodes[0].Transform[5] != 0 || scene.Nodes[1].Transform[4] != 160 || scene.Nodes[1].Transform[5] != 20 {
		t.Fatalf("unexpected placement: %#v", scene.Nodes)
	}
}

func rasterNodeV3(id string, assetID uuid.UUID) NodeV2 {
	return NodeV2{
		ID: id, Type: "raster", ParentID: nil, OrderKey: "00000000",
		Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1,
		BlendMode: "normal", Visible: true, AssetID: &assetID, Effects: []EffectV2{},
	}
}
