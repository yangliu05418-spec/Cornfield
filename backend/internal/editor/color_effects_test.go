package editor

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestColorMatrixV1MatchesSharedFixture(t *testing.T) {
	var fixture struct {
		Effects []EffectV2 `json:"effects"`
		Input   [4]float64 `json:"input"`
		Output  [4]float64 `json:"output"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "editor", "color-effects-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	red, green, blue, alpha := ApplyColorMatrixV1(CompileColorMatrixV1(fixture.Effects), fixture.Input[0], fixture.Input[1], fixture.Input[2], fixture.Input[3])
	assertChannels(t, [4]float64{red, green, blue, alpha}, fixture.Output, .000000000001)
}

func TestCompileColorMatrixV1AppliesEffectsInDocumentOrder(t *testing.T) {
	effects := []EffectV2{
		{Type: "exposure", Version: 1, Enabled: true, Parameters: map[string]float64{"stops": 1}},
		{Type: "contrast", Version: 1, Enabled: true, Parameters: map[string]float64{"amount": 1}},
		{Type: "saturation", Version: 1, Enabled: false, Parameters: map[string]float64{"amount": -1}},
	}
	red, green, blue, alpha := ApplyColorMatrixV1(CompileColorMatrixV1(effects), .2, .3, .4, .75)
	assertChannels(t, [4]float64{red, green, blue, alpha}, [4]float64{.3, .7, 1, .75}, .000001)
}

func TestCompileColorMatrixV1TemperatureAndDesaturation(t *testing.T) {
	effects := []EffectV2{
		{Type: "temperature", Version: 1, Enabled: true, Parameters: map[string]float64{"kelvin_delta": 5000}},
		{Type: "saturation", Version: 1, Enabled: true, Parameters: map[string]float64{"amount": -1}},
	}
	red, green, blue, alpha := ApplyColorMatrixV1(CompileColorMatrixV1(effects), .8, .4, .2, 1)
	warmedLuma := .2126*(.8*1.1) + .7152*(.4*1.025) + .0722*(.2*.9)
	assertChannels(t, [4]float64{red, green, blue, alpha}, [4]float64{warmedLuma, warmedLuma, warmedLuma, 1}, .000001)
}

func assertChannels(t *testing.T, got, want [4]float64, tolerance float64) {
	t.Helper()
	for index := range got {
		if math.Abs(got[index]-want[index]) > tolerance {
			t.Fatalf("channel %d = %f, want %f", index, got[index], want[index])
		}
	}
}
