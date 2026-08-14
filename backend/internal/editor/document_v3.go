package editor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	MaxDocumentV3Bytes       = 2 << 20
	MaxArtboardsV3           = 32
	MaxWorkspaceCoordinateV3 = 1_000_000
)

type DocumentV3 struct {
	SchemaVersion            int          `json:"schema_version"`
	RendererSemanticsVersion int          `json:"renderer_semantics_version"`
	ActiveArtboardID         string       `json:"active_artboard_id"`
	Artboards                []ArtboardV3 `json:"artboards"`
}

type ArtboardV3 struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	OrderKey string   `json:"order_key"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	Visible  bool     `json:"visible"`
	Locked   bool     `json:"locked"`
	Nodes    []NodeV2 `json:"nodes"`
}

func DecodeV3(raw []byte) (DocumentV3, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentV3Bytes {
		if len(raw) > MaxDocumentV3Bytes {
			return DocumentV3{}, ErrDocumentTooLarge
		}
		return DocumentV3{}, ErrInvalidDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document DocumentV3
	if err := decoder.Decode(&document); err != nil {
		return DocumentV3{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DocumentV3{}, ErrInvalidDocument
	}
	if err := document.Validate(); err != nil {
		return DocumentV3{}, err
	}
	return document, nil
}

func (d DocumentV3) Validate() error {
	if d.SchemaVersion != 3 || d.RendererSemanticsVersion != 2 || len(d.Artboards) < 1 || len(d.Artboards) > MaxArtboardsV3 {
		return ErrInvalidDocument
	}
	artboards := make(map[string]struct{}, len(d.Artboards))
	orderKeys := make(map[string]struct{}, len(d.Artboards))
	nodeIDs := make(map[string]struct{})
	totalNodes := 0
	for _, artboard := range d.Artboards {
		if !validNodeID(artboard.ID) || !validArtboardNameV3(artboard.Name) || !validOrderKey(artboard.OrderKey) || !validWorkspaceCoordinateV3(artboard.X) || !validWorkspaceCoordinateV3(artboard.Y) || !validCanvas(Canvas{Width: artboard.Width, Height: artboard.Height}) {
			return ErrInvalidDocument
		}
		if _, exists := artboards[artboard.ID]; exists {
			return ErrInvalidDocument
		}
		if _, exists := orderKeys[artboard.OrderKey]; exists {
			return ErrInvalidDocument
		}
		artboards[artboard.ID] = struct{}{}
		orderKeys[artboard.OrderKey] = struct{}{}
		totalNodes += len(artboard.Nodes)
		if totalNodes > MaxNodesV2 {
			return ErrInvalidDocument
		}
		if len(artboard.Nodes) > 0 {
			v2 := DocumentV2{SchemaVersion: 2, RendererSemanticsVersion: 1, Canvas: Canvas{Width: artboard.Width, Height: artboard.Height}, Nodes: artboard.Nodes}
			if err := v2.Validate(); err != nil {
				return err
			}
		}
		for _, node := range artboard.Nodes {
			if _, exists := nodeIDs[node.ID]; exists {
				return ErrInvalidDocument
			}
			nodeIDs[node.ID] = struct{}{}
		}
	}
	if _, exists := artboards[d.ActiveArtboardID]; !exists {
		return ErrInvalidDocument
	}
	return nil
}

func validArtboardNameV3(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 64
}

func validWorkspaceCoordinateV3(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= MaxWorkspaceCoordinateV3
}

func MigrateV1ToV3(document Document) (DocumentV3, error) {
	v2, err := MigrateV1ToV2(document)
	if err != nil {
		return DocumentV3{}, err
	}
	return MigrateV2ToV3(v2)
}

func MigrateV2ToV3(document DocumentV2) (DocumentV3, error) {
	if err := document.Validate(); err != nil {
		return DocumentV3{}, err
	}
	artboard := ArtboardV3{
		ID: "artboard-1", Name: "画板 1", OrderKey: "00000000",
		Width: document.Canvas.Width, Height: document.Canvas.Height,
		Visible: true, Nodes: append([]NodeV2(nil), document.Nodes...),
	}
	result := DocumentV3{
		SchemaVersion: 3, RendererSemanticsVersion: 2,
		ActiveArtboardID: artboard.ID, Artboards: []ArtboardV3{artboard},
	}
	return result, result.Validate()
}

func (d DocumentV3) Artboard(id string) (ArtboardV3, bool) {
	for _, artboard := range d.Artboards {
		if artboard.ID == id {
			return artboard, true
		}
	}
	return ArtboardV3{}, false
}

func (d DocumentV3) AssetIDs() []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for _, artboard := range d.Artboards {
		for _, node := range artboard.Nodes {
			if node.AssetID == nil {
				continue
			}
			if _, exists := seen[*node.AssetID]; exists {
				continue
			}
			seen[*node.AssetID] = struct{}{}
			ids = append(ids, *node.AssetID)
		}
	}
	return ids
}

func (d DocumentV3) PixelMaskReferences() []PixelMaskReferenceV2 {
	references := make([]PixelMaskReferenceV2, 0)
	for _, artboard := range d.Artboards {
		for _, node := range artboard.Nodes {
			if node.PixelMask == nil || node.AssetID == nil {
				continue
			}
			references = append(references, PixelMaskReferenceV2{
				NodeID: node.ID, AssetID: *node.AssetID,
				ResourceID: node.PixelMask.ResourceID, Version: node.PixelMask.Version,
			})
		}
	}
	return references
}

func (d DocumentV3) OrderedArtboards(ids []string) ([]ArtboardV3, error) {
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, exists := selected[id]; exists {
			return nil, ErrInvalidDocument
		}
		selected[id] = struct{}{}
	}
	result := make([]ArtboardV3, 0, len(ids))
	for _, artboard := range d.Artboards {
		if len(selected) == 0 {
			if artboard.ID == d.ActiveArtboardID {
				result = append(result, artboard)
			}
			continue
		}
		if _, exists := selected[artboard.ID]; exists {
			result = append(result, artboard)
		}
	}
	if len(result) != max(1, len(ids)) {
		return nil, ErrInvalidDocument
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].OrderKey == result[j].OrderKey {
			return result[i].ID < result[j].ID
		}
		return result[i].OrderKey < result[j].OrderKey
	})
	return result, nil
}
