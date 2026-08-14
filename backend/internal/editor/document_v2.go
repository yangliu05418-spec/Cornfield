package editor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxDocumentV2Bytes = 2 << 20
	MaxNodesV2         = 500
	MaxNodeDepthV2     = 32
)

var ErrUnsupportedDocumentSemantics = errors.New("unsupported editor document semantics")

// DecodedDocument is the version-neutral boundary used by persistence and
// authorization code. Rendering remains deliberately explicit: V2 documents
// must compile without loss to the currently authoritative V1 renderer.
type DecodedDocument struct {
	SchemaVersion int
	V1            *Document
	V2            *DocumentV2
}

func DecodeAny(raw []byte) (DecodedDocument, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentV2Bytes {
		if len(raw) > MaxDocumentV2Bytes {
			return DecodedDocument{}, ErrDocumentTooLarge
		}
		return DecodedDocument{}, ErrInvalidDocument
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return DecodedDocument{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	switch header.SchemaVersion {
	case 1:
		document, err := Decode(raw)
		if err != nil {
			return DecodedDocument{}, err
		}
		return DecodedDocument{SchemaVersion: 1, V1: &document}, nil
	case 2:
		document, err := DecodeV2(raw)
		if err != nil {
			return DecodedDocument{}, err
		}
		return DecodedDocument{SchemaVersion: 2, V2: &document}, nil
	default:
		return DecodedDocument{}, ErrInvalidDocument
	}
}

func DecodeRenderable(raw []byte) (Document, error) {
	document, err := DecodeAny(raw)
	if err != nil {
		return Document{}, err
	}
	return document.RenderableV1()
}

func (d DecodedDocument) RenderableV1() (Document, error) {
	switch d.SchemaVersion {
	case 1:
		if d.V1 == nil {
			return Document{}, ErrInvalidDocument
		}
		return *d.V1, nil
	case 2:
		if d.V2 == nil {
			return Document{}, ErrInvalidDocument
		}
		return d.V2.ToV1()
	default:
		return Document{}, ErrInvalidDocument
	}
}

func (d DecodedDocument) AssetIDs() []uuid.UUID {
	switch d.SchemaVersion {
	case 1:
		if d.V1 != nil {
			return d.V1.AssetIDs()
		}
	case 2:
		if d.V2 != nil {
			return d.V2.AssetIDs()
		}
	}
	return nil
}

func (d DecodedDocument) PixelMaskReferences() []PixelMaskReferenceV2 {
	if d.SchemaVersion != 2 || d.V2 == nil {
		return nil
	}
	references := make([]PixelMaskReferenceV2, 0)
	for _, node := range d.V2.Nodes {
		if node.PixelMask == nil {
			continue
		}
		references = append(references, PixelMaskReferenceV2{
			NodeID: node.ID, AssetID: *node.AssetID,
			ResourceID: node.PixelMask.ResourceID, Version: node.PixelMask.Version,
		})
	}
	return references
}

type DocumentV2 struct {
	SchemaVersion            int      `json:"schema_version"`
	RendererSemanticsVersion int      `json:"renderer_semantics_version"`
	Canvas                   Canvas   `json:"canvas"`
	Nodes                    []NodeV2 `json:"nodes"`
}

type NodeV2 struct {
	ID        string       `json:"id"`
	Type      string       `json:"type"`
	Name      string       `json:"name,omitempty"`
	ParentID  *string      `json:"parent_id"`
	OrderKey  string       `json:"order_key"`
	Transform [6]float64   `json:"transform"`
	Opacity   float64      `json:"opacity"`
	BlendMode string       `json:"blend_mode"`
	Visible   bool         `json:"visible"`
	Locked    bool         `json:"locked"`
	MaskID    *string      `json:"mask_id,omitempty"`
	AssetID   *uuid.UUID   `json:"asset_id,omitempty"`
	Crop      *Crop        `json:"crop,omitempty"`
	Effects   []EffectV2   `json:"effects"`
	TargetID  *string      `json:"target_id,omitempty"`
	ShapeMask *ShapeMaskV2 `json:"shape_mask,omitempty"`
	PixelMask *PixelMaskV2 `json:"pixel_mask,omitempty"`
}

type PixelMaskV2 struct {
	ResourceID uuid.UUID `json:"resource_id"`
	Version    int64     `json:"version"`
}

type PixelMaskReferenceV2 struct {
	NodeID     string
	AssetID    uuid.UUID
	ResourceID uuid.UUID
	Version    int64
}

type ShapeMaskV2 struct {
	Type     string  `json:"type"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Inverted bool    `json:"inverted"`
}

type EffectV2 struct {
	Type       string             `json:"type"`
	Version    int                `json:"version"`
	Enabled    bool               `json:"enabled"`
	Parameters map[string]float64 `json:"parameters"`
}

func DecodeV2(raw []byte) (DocumentV2, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentV2Bytes {
		if len(raw) > MaxDocumentV2Bytes {
			return DocumentV2{}, ErrDocumentTooLarge
		}
		return DocumentV2{}, ErrInvalidDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document DocumentV2
	if err := decoder.Decode(&document); err != nil {
		return DocumentV2{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DocumentV2{}, ErrInvalidDocument
	}
	if err := document.Validate(); err != nil {
		return DocumentV2{}, err
	}
	return document, nil
}

func MigrateV1ToV2(document Document) (DocumentV2, error) {
	if err := document.Validate(); err != nil {
		return DocumentV2{}, err
	}
	objects := append([]Object(nil), document.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].ZIndex < objects[j].ZIndex })
	nodes := make([]NodeV2, len(objects))
	for index, object := range objects {
		assetID := object.AssetID
		nodes[index] = NodeV2{
			ID: object.ID, Type: "raster", Name: object.Name,
			OrderKey: fmt.Sprintf("%08d", index), Transform: object.Transform,
			Opacity: object.Opacity, BlendMode: "normal", Visible: object.Visible, Locked: object.Locked,
			AssetID: &assetID, Crop: object.Crop, Effects: []EffectV2{},
		}
	}
	return DocumentV2{
		SchemaVersion: 2, RendererSemanticsVersion: 1,
		Canvas: document.Canvas, Nodes: nodes,
	}, nil
}

func (d DocumentV2) ToV1() (Document, error) {
	if err := d.Validate(); err != nil {
		return Document{}, err
	}
	nodes := append([]NodeV2(nil), d.Nodes...)
	for _, node := range nodes {
		if node.Type != "raster" || node.ParentID != nil || node.MaskID != nil || node.ShapeMask != nil || node.PixelMask != nil || node.BlendMode != "normal" || len(node.Effects) != 0 || node.AssetID == nil {
			return Document{}, ErrUnsupportedDocumentSemantics
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].OrderKey == nodes[j].OrderKey {
			return nodes[i].ID < nodes[j].ID
		}
		return nodes[i].OrderKey < nodes[j].OrderKey
	})
	objects := make([]Object, len(nodes))
	for index, node := range nodes {
		objects[index] = Object{
			ID: node.ID, Name: node.Name, AssetID: *node.AssetID, Transform: node.Transform,
			Opacity: node.Opacity, Visible: node.Visible, Locked: node.Locked, ZIndex: index, Crop: node.Crop,
		}
	}
	document := Document{SchemaVersion: 1, Canvas: d.Canvas, Objects: objects}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

func (d DocumentV2) Validate() error {
	if d.SchemaVersion != 2 || d.RendererSemanticsVersion != 1 || !validCanvas(d.Canvas) || len(d.Nodes) < 1 || len(d.Nodes) > MaxNodesV2 {
		return ErrInvalidDocument
	}
	nodes := make(map[string]NodeV2, len(d.Nodes))
	orderKeys := make(map[string]struct{}, len(d.Nodes))
	for _, node := range d.Nodes {
		if !validNodeID(node.ID) || !validNodeName(node.Name) || !validOrderKey(node.OrderKey) || !validTransform(node.Transform) || !validOpacity(node.Opacity) || !validBlendMode(node.BlendMode) {
			return ErrInvalidDocument
		}
		if _, exists := nodes[node.ID]; exists {
			return ErrInvalidDocument
		}
		parent := "\x00"
		if node.ParentID != nil {
			if *node.ParentID == node.ID {
				return ErrInvalidDocument
			}
			parent = "\x01" + *node.ParentID
		}
		orderIdentity := parent + "\x00" + node.OrderKey
		if _, exists := orderKeys[orderIdentity]; exists {
			return ErrInvalidDocument
		}
		orderKeys[orderIdentity] = struct{}{}
		if node.Type != "raster" && node.Type != "group" && node.Type != "adjustment" {
			return ErrInvalidDocument
		}
		if node.Type == "raster" {
			if node.AssetID == nil || *node.AssetID == uuid.Nil || node.TargetID != nil || !validCrop(node.Crop) || !validEffects(node.Effects) || !validShapeMaskV2(node.ShapeMask) || !validPixelMaskV2(node.PixelMask) || (node.ShapeMask != nil && (node.Crop != nil || node.MaskID != nil || node.PixelMask != nil)) || (node.PixelMask != nil && node.MaskID != nil) {
				return ErrInvalidDocument
			}
		} else if node.Type == "group" {
			if node.AssetID != nil || node.Crop != nil || node.TargetID != nil || node.ShapeMask != nil || node.PixelMask != nil || len(node.Effects) != 0 {
				return ErrInvalidDocument
			}
		} else if node.AssetID != nil || node.Crop != nil || node.MaskID != nil || node.ShapeMask != nil || node.PixelMask != nil || node.TargetID == nil || *node.TargetID == node.ID || node.Transform != [6]float64{1, 0, 0, 1, 0, 0} || node.BlendMode != "normal" || !validEffects(node.Effects) {
			return ErrInvalidDocument
		}
		nodes[node.ID] = node
	}
	for _, node := range d.Nodes {
		if node.ParentID != nil {
			parent, exists := nodes[*node.ParentID]
			if !exists || parent.Type != "group" {
				return ErrInvalidDocument
			}
		}
		if node.MaskID != nil {
			mask, exists := nodes[*node.MaskID]
			if !exists || mask.Type != "raster" || mask.ID == node.ID {
				return ErrInvalidDocument
			}
		}
		if node.Type == "adjustment" {
			target, exists := nodes[*node.TargetID]
			if !exists || target.Type != "raster" || target.ParentID == nil != (node.ParentID == nil) || (target.ParentID != nil && *target.ParentID != *node.ParentID) {
				return ErrInvalidDocument
			}
			for _, candidate := range d.Nodes {
				if candidate.MaskID != nil && *candidate.MaskID == target.ID {
					return ErrInvalidDocument
				}
			}
		}
		if node.ShapeMask != nil {
			for _, candidate := range d.Nodes {
				if candidate.MaskID != nil && *candidate.MaskID == node.ID {
					return ErrInvalidDocument
				}
			}
		}
		if node.PixelMask != nil {
			for _, candidate := range d.Nodes {
				if candidate.MaskID != nil && *candidate.MaskID == node.ID {
					return ErrInvalidDocument
				}
			}
		}
		if depth, ok := nodeDepth(node, nodes); !ok || depth > MaxNodeDepthV2 {
			return ErrInvalidDocument
		}
		if !validMaskChain(node, nodes) {
			return ErrInvalidDocument
		}
	}
	return nil
}

func (d DocumentV2) AssetIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(d.Nodes))
	seen := make(map[uuid.UUID]struct{}, len(d.Nodes))
	for _, node := range d.Nodes {
		if node.AssetID == nil {
			continue
		}
		if _, exists := seen[*node.AssetID]; exists {
			continue
		}
		seen[*node.AssetID] = struct{}{}
		ids = append(ids, *node.AssetID)
	}
	return ids
}

func validCanvas(canvas Canvas) bool {
	return canvas.Width >= 1 && canvas.Height >= 1 && canvas.Width <= 8192 && canvas.Height <= 8192 && int64(canvas.Width)*int64(canvas.Height) <= MaxCanvasPixels
}

func validNodeID(value string) bool {
	return len(value) >= 1 && len(value) <= 64 && strings.TrimSpace(value) == value && utf8.ValidString(value)
}

func validNodeName(value string) bool {
	return len(value) <= 256 && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 64
}

func validOrderKey(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z')) {
			return false
		}
	}
	return true
}

func validTransform(transform [6]float64) bool {
	for _, value := range transform {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1_000_000 {
			return false
		}
	}
	return math.Abs(transform[0]*transform[3]-transform[1]*transform[2]) >= 1e-8
}

func validOpacity(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validBlendMode(value string) bool {
	switch value {
	case "normal", "multiply", "screen", "overlay", "darken", "lighten":
		return true
	default:
		return false
	}
}

func validCrop(crop *Crop) bool {
	if crop == nil {
		return true
	}
	sum := crop.X + crop.Y + crop.Width + crop.Height
	return crop.X >= 0 && crop.Y >= 0 && crop.Width > 0 && crop.Height > 0 && crop.X+crop.Width <= 1 && crop.Y+crop.Height <= 1 && !math.IsNaN(sum) && !math.IsInf(sum, 0)
}

func validShapeMaskV2(mask *ShapeMaskV2) bool {
	if mask == nil {
		return true
	}
	sum := mask.X + mask.Y + mask.Width + mask.Height
	return (mask.Type == "rectangle" || mask.Type == "ellipse") && mask.X >= 0 && mask.Y >= 0 && mask.Width > 0 && mask.Height > 0 && mask.X+mask.Width <= 1 && mask.Y+mask.Height <= 1 && !math.IsNaN(sum) && !math.IsInf(sum, 0)
}

func validPixelMaskV2(mask *PixelMaskV2) bool {
	return mask == nil || (mask.ResourceID != uuid.Nil && mask.Version >= 0)
}

func validEffects(effects []EffectV2) bool {
	if len(effects) > 16 {
		return false
	}
	for _, effect := range effects {
		if effect.Version != 1 || len(effect.Parameters) == 0 {
			return false
		}
		var allowed map[string][2]float64
		switch effect.Type {
		case "exposure":
			allowed = map[string][2]float64{"stops": {-5, 5}}
		case "contrast", "saturation":
			allowed = map[string][2]float64{"amount": {-1, 1}}
		case "temperature":
			allowed = map[string][2]float64{"kelvin_delta": {-10000, 10000}}
		default:
			return false
		}
		if len(effect.Parameters) != len(allowed) {
			return false
		}
		for key, value := range effect.Parameters {
			bounds, exists := allowed[key]
			if !exists || math.IsNaN(value) || math.IsInf(value, 0) || value < bounds[0] || value > bounds[1] {
				return false
			}
		}
	}
	return true
}

func nodeDepth(node NodeV2, nodes map[string]NodeV2) (int, bool) {
	seen := map[string]struct{}{node.ID: {}}
	depth := 1
	for node.ParentID != nil {
		if _, exists := seen[*node.ParentID]; exists {
			return 0, false
		}
		seen[*node.ParentID] = struct{}{}
		parent, exists := nodes[*node.ParentID]
		if !exists {
			return 0, false
		}
		node = parent
		depth++
	}
	return depth, true
}

func validMaskChain(node NodeV2, nodes map[string]NodeV2) bool {
	seen := map[string]struct{}{node.ID: {}}
	for node.MaskID != nil {
		if _, exists := seen[*node.MaskID]; exists {
			return false
		}
		seen[*node.MaskID] = struct{}{}
		node = nodes[*node.MaskID]
	}
	return true
}
