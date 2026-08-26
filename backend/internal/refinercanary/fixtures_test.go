package refinercanary

import (
	"testing"
	"unicode/utf8"
)

func TestFixtureCorpusShape(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 50 {
		t.Fatalf("fixture count = %d, want 50", len(fixtures))
	}
	wantClasses := map[string]int{
		"output_contract":     12,
		"prompt_injection":    10,
		"semantic_drift":      10,
		"provider_length":     8,
		"safety_filter":       6,
		"reliability_privacy": 4,
	}
	gotClasses := make(map[string]int, len(wantClasses))
	for _, fixture := range fixtures {
		gotClasses[fixture.Class]++
	}
	for class, want := range wantClasses {
		if got := gotClasses[class]; got != want {
			t.Errorf("class %q count = %d, want %d", class, got, want)
		}
	}
	if got := len(ProtocolFixtures(fixtures)); got != 5 {
		t.Errorf("protocol fixture count = %d, want 5", got)
	}
	if got := len(E2EFixtures(fixtures)); got != 10 {
		t.Errorf("e2e fixture count = %d, want 10", got)
	}
}

func TestMidjourneyLiveBoundaryFixtureIsNaturalAndNearLimit(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if fixture.ID != "limit.midjourney-exact" {
			continue
		}
		runes := utf8.RuneCountInString(fixture.Original)
		if runes < 900 || runes > fixture.MaxRunes {
			t.Fatalf("boundary fixture runes = %d, want 900..%d", runes, fixture.MaxRunes)
		}
		if fixture.MinimumResultRunes < 850 || fixture.MustMatchOriginal {
			t.Fatalf("boundary fixture invariants are too weak or brittle: %#v", fixture)
		}
		if fixture.Candidate != fixture.Original {
			t.Fatal("synthetic provider candidate must retain the full boundary prompt")
		}
		return
	}
	t.Fatal("missing Midjourney live boundary fixture")
}

func TestFixtureContentIsSyntheticAndReportSafe(t *testing.T) {
	fixtures, err := Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if fixture.Original == fixture.ID || fixture.Original == fixture.Class {
			t.Errorf("fixture %q does not contain a useful synthetic prompt", fixture.ID)
		}
		if fixture.LiveProtocol && fixture.ExpectedProvider != "accept" {
			t.Errorf("live fixture %q does not exercise a successful provider response", fixture.ID)
		}
	}
}
