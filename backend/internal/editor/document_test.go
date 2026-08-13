package editor

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDocumentRoundTripAndValidation(t *testing.T) {
	document := New(uuid.New(), 2048, 1024)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Canvas != document.Canvas || len(decoded.Objects) != 1 {
		t.Fatalf("unexpected decoded document: %#v", decoded)
	}
	decoded.Objects[0].Transform = [6]float64{}
	if err := decoded.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("singular transform error = %v", err)
	}
}

func TestDocumentRejectsUnknownAndOversizedInput(t *testing.T) {
	if _, err := Decode([]byte(`{"schema_version":1,"canvas":{"width":1,"height":1},"objects":[],"url":"https://example.com"}`)); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := Decode(make([]byte, MaxDocumentBytes+1)); !errors.Is(err, ErrDocumentTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestDocumentRejectsOversizedCanvasAllocation(t *testing.T) {
	document := New(uuid.New(), 8192, 8192)
	if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("Validate() error = %v, want ErrInvalidDocument", err)
	}
	document.Canvas = Canvas{Width: 6000, Height: 6000}
	if err := document.Validate(); err != nil {
		t.Fatalf("36 megapixel boundary rejected: %v", err)
	}
}

func TestDocumentObjectLimitIsIndependentFromProviderLayerLimit(t *testing.T) {
	document := New(uuid.New(), 1024, 1024)
	base := document.Objects[0]
	for index := 1; index < MaxObjects; index++ {
		object := base
		object.ID = uuid.NewString()
		object.ZIndex = index
		document.Objects = append(document.Objects, object)
	}
	if err := document.Validate(); err != nil {
		t.Fatalf("%d editor objects rejected: %v", MaxObjects, err)
	}
	extra := base
	extra.ID = uuid.NewString()
	extra.ZIndex = MaxObjects
	document.Objects = append(document.Objects, extra)
	if err := document.Validate(); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("%d editor objects error = %v, want ErrInvalidDocument", MaxObjects+1, err)
	}
	if MaxProviderLayers >= MaxObjects {
		t.Fatalf("provider layer limit %d must remain below editor object limit %d", MaxProviderLayers, MaxObjects)
	}
}
