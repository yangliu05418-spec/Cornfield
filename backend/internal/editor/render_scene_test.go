package editor

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestCompileV2RenderSceneMatchesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "editor", "v2-group-mask.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document DocumentV2
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	scene, err := CompileV2RenderScene(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Nodes) != 2 {
		t.Fatalf("node count = %d", len(scene.Nodes))
	}
	mask, content := scene.Nodes[0], scene.Nodes[1]
	if mask.ID != "mask" || mask.Role != RenderRoleMask || mask.Transform != [6]float64{1, 0, 0, 1, 76, 66} || mask.Opacity != .65 {
		t.Fatalf("unexpected mask: %#v", mask)
	}
	if content.ID != "content" || content.Role != RenderRoleContent || content.Transform != [6]float64{0, 1.2, -1.2, 0, 199.2, 57.6} || content.Opacity < .559999 || content.Opacity > .560001 || content.MaskNodeID == nil || *content.MaskNodeID != "mask" {
		t.Fatalf("unexpected content: %#v", content)
	}
}

func TestCompileV2RenderSceneGroupsAndMasks(t *testing.T) {
	maskAsset, contentAsset := uuid.New(), uuid.New()
	groupID, maskID := "group", "mask"
	document := DocumentV2{
		SchemaVersion: 2, RendererSemanticsVersion: 1,
		Canvas: Canvas{Width: 256, Height: 256},
		Nodes: []NodeV2{
			{ID: maskID, Type: "raster", OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 76, 66}, Opacity: .65, BlendMode: "normal", Visible: true, AssetID: &maskAsset, Effects: []EffectV2{}},
			{ID: groupID, Type: "group", OrderKey: "00000002", Transform: [6]float64{0, 1.2, -1.2, 0, 204, 54}, Opacity: .7, BlendMode: "normal", Visible: true},
			{ID: "content", Type: "raster", ParentID: &groupID, OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 3, 4}, Opacity: .8, BlendMode: "normal", Visible: true, MaskID: &maskID, AssetID: &contentAsset, Effects: []EffectV2{}},
		},
	}
	scene, err := CompileV2RenderScene(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Nodes) != 2 || scene.Nodes[0].Role != RenderRoleMask || scene.Nodes[1].Role != RenderRoleContent {
		t.Fatalf("unexpected nodes: %#v", scene.Nodes)
	}
	content := scene.Nodes[1]
	if content.Transform != [6]float64{0, 1.2, -1.2, 0, 199.2, 57.6} || content.Opacity < .559999 || content.Opacity > .560001 {
		t.Fatalf("group state was not accumulated: %#v", content)
	}
	if content.MaskNodeID == nil || *content.MaskNodeID != maskID {
		t.Fatalf("mask reference lost: %#v", content.MaskNodeID)
	}
}

func TestCompileV2RenderSceneCarriesRasterBlendAndEffects(t *testing.T) {
	asset := uuid.New()
	effects := []EffectV2{{Type: "contrast", Version: 1, Enabled: true, Parameters: map[string]float64{"amount": .2}}}
	node := NodeV2{ID: "content", Type: "raster", OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "multiply", Visible: true, AssetID: &asset, Effects: effects}
	scene, err := CompileV2RenderScene(DocumentV2{SchemaVersion: 2, RendererSemanticsVersion: 1, Canvas: Canvas{Width: 10, Height: 10}, Nodes: []NodeV2{node}})
	if err != nil {
		t.Fatal(err)
	}
	if got := scene.Nodes[0]; got.BlendMode != "multiply" || len(got.Effects) != 1 || got.Effects[0].Type != "contrast" {
		t.Fatalf("render semantics lost: %#v", got)
	}
	effects[0].Parameters["amount"] = .8
	if scene.Nodes[0].Effects[0].Parameters["amount"] != .2 {
		t.Fatal("render effects were not cloned")
	}
}

func TestCompileV2RenderSceneCollapsesClippedAdjustmentsIntoTargetMatrix(t *testing.T) {
	asset := uuid.New()
	targetID := "content"
	targetEffects := []EffectV2{{Type: "exposure", Version: 1, Enabled: true, Parameters: map[string]float64{"stops": .5}}}
	adjustmentEffects := []EffectV2{{Type: "contrast", Version: 1, Enabled: true, Parameters: map[string]float64{"amount": .4}}}
	document := DocumentV2{SchemaVersion: 2, RendererSemanticsVersion: 1, Canvas: Canvas{Width: 10, Height: 10}, Nodes: []NodeV2{
		{ID: targetID, Type: "raster", OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "normal", Visible: true, AssetID: &asset, Effects: targetEffects},
		{ID: "adjustment", Type: "adjustment", TargetID: &targetID, OrderKey: "00000002", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: .5, BlendMode: "normal", Visible: true, Effects: adjustmentEffects},
	}}
	scene, err := CompileV2RenderScene(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(scene.Nodes) != 1 {
		t.Fatalf("node count = %d", len(scene.Nodes))
	}
	want := ComposeColorMatricesV1(CompileColorMatrixV1(targetEffects), CompileColorMatrixWithStrengthV1(adjustmentEffects, .5))
	if scene.Nodes[0].ColorMatrix != want {
		t.Fatalf("color matrix = %#v, want %#v", scene.Nodes[0].ColorMatrix, want)
	}
}

func TestCompileV2RenderSceneRejectsGroupBlend(t *testing.T) {
	group := NodeV2{ID: "group", Type: "group", OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "multiply", Visible: true}
	_, err := CompileV2RenderScene(DocumentV2{SchemaVersion: 2, RendererSemanticsVersion: 1, Canvas: Canvas{Width: 10, Height: 10}, Nodes: []NodeV2{group}})
	if !errors.Is(err, ErrUnsupportedDocumentSemantics) {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileV2RenderSceneRejectsMaskEffects(t *testing.T) {
	maskAsset, contentAsset := uuid.New(), uuid.New()
	maskID := "mask"
	document := DocumentV2{
		SchemaVersion: 2, RendererSemanticsVersion: 1, Canvas: Canvas{Width: 10, Height: 10},
		Nodes: []NodeV2{
			{ID: maskID, Type: "raster", OrderKey: "00000001", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "normal", Visible: true, AssetID: &maskAsset, Effects: []EffectV2{{Type: "contrast", Version: 1, Enabled: true, Parameters: map[string]float64{"amount": .2}}}},
			{ID: "content", Type: "raster", OrderKey: "00000002", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "normal", Visible: true, AssetID: &contentAsset, MaskID: &maskID},
		},
	}
	if _, err := CompileV2RenderScene(document); !errors.Is(err, ErrUnsupportedDocumentSemantics) {
		t.Fatalf("error = %v", err)
	}
}

func TestMultiplyTransformsMatchesCSSAffine(t *testing.T) {
	got := MultiplyTransforms([6]float64{0, 1, -1, 0, 10, 20}, [6]float64{2, 0, 0, 3, 4, 5})
	want := [6]float64{0, 2, -3, 0, 5, 24}
	if got != want {
		t.Fatalf("MultiplyTransforms() = %#v, want %#v", got, want)
	}
}

func TestRenderSceneValidateRequiresMaskRole(t *testing.T) {
	asset, maskAsset := uuid.New(), uuid.New()
	maskID := "mask"
	scene := RenderScene{
		Canvas: Canvas{Width: 10, Height: 10},
		Nodes: []RenderNode{
			{ID: maskID, AssetID: maskAsset, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: true, Order: 0, Role: RenderRoleContent},
			{ID: "content", AssetID: asset, Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, Visible: true, Order: 1, Role: RenderRoleContent, MaskNodeID: &maskID},
		},
	}
	if err := scene.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("error = %v", err)
	}
}
