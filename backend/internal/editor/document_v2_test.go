package editor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestDocumentV2SharedGoldenMigration(t *testing.T) {
	v1Raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "editor", "v1-flat.json"))
	if err != nil {
		t.Fatal(err)
	}
	v2Raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "editor", "v2-flat.json"))
	if err != nil {
		t.Fatal(err)
	}
	v1, err := Decode(v1Raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := MigrateV1ToV2(v1)
	if err != nil {
		t.Fatal(err)
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotCanonical, wantCanonical bytes.Buffer
	if err = json.Compact(&gotCanonical, gotRaw); err != nil {
		t.Fatal(err)
	}
	if err = json.Compact(&wantCanonical, v2Raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotCanonical.Bytes(), wantCanonical.Bytes()) {
		t.Fatalf("migration differs from shared golden\n got: %s\nwant: %s", gotCanonical.Bytes(), wantCanonical.Bytes())
	}
	decoded, err := DecodeAny(v2Raw)
	if err != nil || decoded.SchemaVersion != 2 || len(decoded.AssetIDs()) != 2 {
		t.Fatalf("DecodeAny(V2) = %#v, %v", decoded, err)
	}
}

func TestDecodeAnyPreservesPerVersionSizeLimits(t *testing.T) {
	oversizedV1 := append([]byte(`{"schema_version":1,"canvas":{"width":1,"height":1},"objects":[]}`), bytes.Repeat([]byte(" "), MaxDocumentBytes)...)
	if _, err := DecodeAny(oversizedV1); !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("oversized V1 error = %v", err)
	}
	v2, err := MigrateV1ToV2(New(uuid.New(), 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(v2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeAny(raw); err != nil {
		t.Fatalf("valid V2 error = %v", err)
	}
}

func TestDocumentV2MigratesV1WithoutLosingRenderableSemantics(t *testing.T) {
	v1 := New(uuid.New(), 2048, 1024)
	v1.Objects[0].Crop = &Crop{X: 0.1, Y: 0.2, Width: 0.7, Height: 0.6}
	v1.Objects[0].Opacity = 0.75
	v2, err := MigrateV1ToV2(v1)
	if err != nil {
		t.Fatal(err)
	}
	if v2.SchemaVersion != 2 || v2.RendererSemanticsVersion != 1 || len(v2.Nodes) != 1 || v2.Nodes[0].Type != "raster" {
		t.Fatalf("unexpected V2 document: %#v", v2)
	}
	roundTrip, err := v2.ToV1()
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Objects[0].AssetID != v1.Objects[0].AssetID || roundTrip.Objects[0].Opacity != v1.Objects[0].Opacity || *roundTrip.Objects[0].Crop != *v1.Objects[0].Crop {
		t.Fatalf("round trip changed semantics: %#v", roundTrip)
	}
}

func TestDocumentV2ValidatesTreeEffectsAndExportSubset(t *testing.T) {
	v2, err := MigrateV1ToV2(New(uuid.New(), 1024, 1024))
	if err != nil {
		t.Fatal(err)
	}
	groupID := "group"
	v2.Nodes = append(v2.Nodes, NodeV2{
		ID: groupID, Type: "group", Name: "组", OrderKey: "00000001",
		Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "normal", Visible: true,
	})
	v2.Nodes[0].ParentID = &groupID
	v2.Nodes[0].Effects = []EffectV2{{Type: "exposure", Version: 1, Enabled: true, Parameters: map[string]float64{"stops": 0.5}}}
	if err := v2.Validate(); err != nil {
		t.Fatalf("valid tree rejected: %v", err)
	}
	if _, err := v2.ToV1(); !errors.Is(err, ErrUnsupportedDocumentSemantics) {
		t.Fatalf("professional semantics export error = %v", err)
	}
	v2.Nodes[1].ParentID = &v2.Nodes[0].ID
	if err := v2.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestDocumentV2ValidatesClippedAdjustmentTarget(t *testing.T) {
	v2, err := MigrateV1ToV2(New(uuid.New(), 128, 128))
	if err != nil {
		t.Fatal(err)
	}
	targetID := v2.Nodes[0].ID
	v2.Nodes = append(v2.Nodes, NodeV2{ID: "adjustment", Type: "adjustment", TargetID: &targetID, OrderKey: "00000002", Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: .5, BlendMode: "normal", Visible: true, Effects: []EffectV2{}})
	if err = v2.Validate(); err != nil {
		t.Fatalf("valid adjustment rejected: %v", err)
	}
	missing := "missing"
	v2.Nodes[1].TargetID = &missing
	if err = v2.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("missing target error = %v", err)
	}
}

func TestDocumentV2ValidatesShapeMaskWithoutSilentV1Downgrade(t *testing.T) {
	v2, err := MigrateV1ToV2(New(uuid.New(), 128, 128))
	if err != nil {
		t.Fatal(err)
	}
	v2.Nodes[0].ShapeMask = &ShapeMaskV2{Type: "ellipse", X: .1, Y: .2, Width: .6, Height: .5, Inverted: true}
	if err = v2.Validate(); err != nil {
		t.Fatalf("valid shape mask rejected: %v", err)
	}
	if _, err = v2.ToV1(); !errors.Is(err, ErrUnsupportedDocumentSemantics) {
		t.Fatalf("shape mask downgrade error = %v", err)
	}
	v2.Nodes[0].Crop = &Crop{X: 0, Y: 0, Width: .5, Height: .5}
	if err = v2.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("crop and shape mask conflict = %v", err)
	}
}

func TestDecodeV2RejectsUnknownFieldsAndExcessiveDepth(t *testing.T) {
	v2, err := MigrateV1ToV2(New(uuid.New(), 32, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v2)
	raw[len(raw)-1] = ','
	raw = append(raw, []byte(`"url":"https://example.com"}`)...)
	if _, err := DecodeV2(raw); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("unknown field error = %v", err)
	}
	deep := v2
	deep.Nodes = nil
	var parent *string
	for index := 0; index <= MaxNodeDepthV2; index++ {
		id := fmt.Sprintf("group-%02d", index)
		deep.Nodes = append(deep.Nodes, NodeV2{ID: id, Type: "group", ParentID: parent, OrderKey: fmt.Sprintf("%08d", index), Transform: [6]float64{1, 0, 0, 1, 0, 0}, Opacity: 1, BlendMode: "normal", Visible: true})
		parent = &deep.Nodes[len(deep.Nodes)-1].ID
	}
	if err := deep.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("depth error = %v", err)
	}
}
