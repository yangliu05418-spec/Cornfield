package editor

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/google/uuid"
)

const (
	MaxDocumentBytes  = 256 << 10
	MaxObjects        = 64
	MaxProviderLayers = 17
	MaxCanvasPixels   = 36_000_000
)

var (
	ErrInvalidDocument  = errors.New("invalid editor document")
	ErrDocumentTooLarge = errors.New("editor document too large")
)

type Document struct {
	SchemaVersion int      `json:"schema_version"`
	Canvas        Canvas   `json:"canvas"`
	Objects       []Object `json:"objects"`
}

type Canvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Crop struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Object struct {
	ID        string     `json:"id"`
	AssetID   uuid.UUID  `json:"asset_id"`
	Transform [6]float64 `json:"transform"`
	Opacity   float64    `json:"opacity"`
	Visible   bool       `json:"visible"`
	Locked    bool       `json:"locked"`
	ZIndex    int        `json:"z_index"`
	Crop      *Crop      `json:"crop,omitempty"`
}

func New(sourceAssetID uuid.UUID, width, height int) Document {
	return Document{
		SchemaVersion: 1,
		Canvas:        Canvas{Width: width, Height: height},
		Objects: []Object{{
			ID: "source", AssetID: sourceAssetID,
			Transform: [6]float64{1, 0, 0, 1, 0, 0},
			Opacity:   1, Visible: true, Locked: false, ZIndex: 0,
		}},
	}
}

func Decode(raw []byte) (Document, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes {
		if len(raw) > MaxDocumentBytes {
			return Document{}, ErrDocumentTooLarge
		}
		return Document{}, ErrInvalidDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Document{}, ErrInvalidDocument
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

func (d Document) Validate() error {
	if d.SchemaVersion != 1 || d.Canvas.Width < 1 || d.Canvas.Height < 1 || d.Canvas.Width > 8192 || d.Canvas.Height > 8192 || int64(d.Canvas.Width)*int64(d.Canvas.Height) > MaxCanvasPixels {
		return ErrInvalidDocument
	}
	if len(d.Objects) < 1 || len(d.Objects) > MaxObjects {
		return ErrInvalidDocument
	}
	ids := make(map[string]struct{}, len(d.Objects))
	zIndexes := make(map[int]struct{}, len(d.Objects))
	for _, object := range d.Objects {
		if object.AssetID == uuid.Nil || len(object.ID) < 1 || len(object.ID) > 64 || strings.TrimSpace(object.ID) != object.ID {
			return ErrInvalidDocument
		}
		if _, exists := ids[object.ID]; exists {
			return ErrInvalidDocument
		}
		ids[object.ID] = struct{}{}
		if _, exists := zIndexes[object.ZIndex]; exists || object.ZIndex < 0 || object.ZIndex >= MaxObjects {
			return ErrInvalidDocument
		}
		zIndexes[object.ZIndex] = struct{}{}
		for _, value := range object.Transform {
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1_000_000 {
				return ErrInvalidDocument
			}
		}
		if math.Abs(object.Transform[0]*object.Transform[3]-object.Transform[1]*object.Transform[2]) < 1e-8 {
			return ErrInvalidDocument
		}
		if math.IsNaN(object.Opacity) || math.IsInf(object.Opacity, 0) || object.Opacity < 0 || object.Opacity > 1 {
			return ErrInvalidDocument
		}
		if object.Crop != nil {
			crop := object.Crop
			if crop.X < 0 || crop.Y < 0 || crop.Width <= 0 || crop.Height <= 0 || crop.X+crop.Width > 1 || crop.Y+crop.Height > 1 ||
				math.IsNaN(crop.X+crop.Y+crop.Width+crop.Height) || math.IsInf(crop.X+crop.Y+crop.Width+crop.Height, 0) {
				return ErrInvalidDocument
			}
		}
	}
	return nil
}

func (d Document) AssetIDs() []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(d.Objects))
	seen := make(map[uuid.UUID]struct{}, len(d.Objects))
	for _, object := range d.Objects {
		if _, exists := seen[object.AssetID]; exists {
			continue
		}
		seen[object.AssetID] = struct{}{}
		ids = append(ids, object.AssetID)
	}
	return ids
}
