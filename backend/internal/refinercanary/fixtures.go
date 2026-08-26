package refinercanary

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

//go:embed fixtures.json
var fixtureData []byte

// Fixture contains synthetic data only. Production prompts and model outputs
// must never be copied into this corpus or a canary report.
type Fixture struct {
	ID                string   `json:"id"`
	Class             string   `json:"class"`
	Original          string   `json:"original,omitempty"`
	OriginalRepeat    string   `json:"original_repeat,omitempty"`
	OriginalRunes     int      `json:"original_runes,omitempty"`
	Candidate         string   `json:"candidate,omitempty"`
	CandidateRepeat   string   `json:"candidate_repeat,omitempty"`
	CandidateRunes    int      `json:"candidate_runes,omitempty"`
	RawContent        string   `json:"raw_content,omitempty"`
	TargetProvider    string   `json:"target_provider"`
	TargetModel       string   `json:"target_model"`
	MaxRunes          int      `json:"max_runes"`
	FinishReason      string   `json:"finish_reason,omitempty"`
	Transport         string   `json:"transport,omitempty"`
	ExpectedProvider  string   `json:"expected_provider"`
	ExpectedCode      string   `json:"expected_code,omitempty"`
	ExpectedInvariant string   `json:"expected_invariant,omitempty"`
	MustPreserve      []string `json:"must_preserve,omitempty"`
	MustNotContain    []string `json:"must_not_contain,omitempty"`
	MustMatchOriginal bool     `json:"must_match_original,omitempty"`
	LiveProtocol      bool     `json:"live_protocol,omitempty"`
	E2E               bool     `json:"e2e,omitempty"`
}

func Fixtures() ([]Fixture, error) {
	var fixtures []Fixture
	if err := json.Unmarshal(fixtureData, &fixtures); err != nil {
		return nil, fmt.Errorf("decode prompt refiner fixtures: %w", err)
	}
	seen := make(map[string]struct{}, len(fixtures))
	for index := range fixtures {
		fixture := &fixtures[index]
		if fixture.ID == "" || fixture.Class == "" || fixture.TargetProvider == "" || fixture.TargetModel == "" || fixture.MaxRunes < 1 {
			return nil, fmt.Errorf("prompt refiner fixture %d is incomplete", index)
		}
		if _, duplicate := seen[fixture.ID]; duplicate {
			return nil, fmt.Errorf("duplicate prompt refiner fixture %q", fixture.ID)
		}
		seen[fixture.ID] = struct{}{}
		if fixture.OriginalRunes > 0 {
			fixture.Original = strings.Repeat(fixture.OriginalRepeat, fixture.OriginalRunes)
		}
		if fixture.CandidateRunes > 0 {
			fixture.Candidate = strings.Repeat(fixture.CandidateRepeat, fixture.CandidateRunes)
		}
		if fixture.Original == "" {
			return nil, fmt.Errorf("prompt refiner fixture %q has an empty original", fixture.ID)
		}
		if fixture.FinishReason == "" {
			fixture.FinishReason = "stop"
		}
		if fixture.Transport == "" {
			fixture.Transport = "ok"
		}
		if fixture.RawContent == "" && fixture.Candidate != "" {
			encoded, err := json.Marshal(map[string]string{"prompt": fixture.Candidate})
			if err != nil {
				return nil, err
			}
			fixture.RawContent = string(encoded)
		}
	}
	return fixtures, nil
}

func ProtocolFixtures(fixtures []Fixture) []Fixture {
	result := make([]Fixture, 0, 5)
	for _, fixture := range fixtures {
		if fixture.LiveProtocol {
			result = append(result, fixture)
		}
	}
	return result
}

func E2EFixtures(fixtures []Fixture) []Fixture {
	result := make([]Fixture, 0, 10)
	for _, fixture := range fixtures {
		if fixture.E2E {
			result = append(result, fixture)
		}
	}
	return result
}

// ValidateInvariant performs only deterministic canary assertions. It does not
// claim semantic equivalence and never returns either prompt in its error.
func ValidateInvariant(fixture Fixture, candidate string) error {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || !utf8.ValidString(candidate) || utf8.RuneCountInString(candidate) > fixture.MaxRunes {
		return errors.New("candidate_boundary")
	}
	if hasDisallowedControl(candidate) {
		return errors.New("candidate_control")
	}
	if fixture.MustMatchOriginal && candidate != strings.TrimSpace(fixture.Original) {
		return errors.New("candidate_not_unchanged")
	}
	if fixture.TargetProvider == "legnext" && hasControlledMidjourneyInput(candidate) {
		return errors.New("candidate_provider_syntax")
	}
	for _, value := range fixture.MustPreserve {
		if !strings.Contains(candidate, value) {
			return errors.New("candidate_lost_anchor")
		}
	}
	lower := strings.ToLower(candidate)
	for _, value := range fixture.MustNotContain {
		if strings.Contains(lower, strings.ToLower(value)) {
			return errors.New("candidate_retained_forbidden_text")
		}
	}
	originalRunes := []rune(strings.TrimSpace(fixture.Original))
	candidateRunes := []rune(candidate)
	if len(originalRunes) >= 80 {
		if len(candidateRunes) > len(originalRunes)+max(64, len(originalRunes)/4) {
			return errors.New("candidate_length_drift")
		}
		if fixture.Class == "semantic_drift" && len(candidateRunes) < len(originalRunes)/2 {
			return errors.New("candidate_length_drift")
		}
	}
	originalHan, originalLatin := scriptCounts(originalRunes)
	candidateHan, candidateLatin := scriptCounts(candidateRunes)
	if originalHan > originalLatin*3 && candidateLatin > candidateHan || originalLatin > originalHan*3 && candidateHan > candidateLatin {
		return errors.New("candidate_language_drift")
	}
	return nil
}

func hasDisallowedControl(value string) bool {
	for _, character := range value {
		if character != '\n' && character != '\t' && (character < 0x20 || character == 0x7f) {
			return true
		}
	}
	return false
}

func hasControlledMidjourneyInput(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://") ||
		strings.Contains(value, "{") || strings.Contains(value, "}") ||
		strings.Contains(lower, "--")
}

func scriptCounts(value []rune) (han, latin int) {
	for _, character := range value {
		switch {
		case unicode.Is(unicode.Han, character):
			han++
		case unicode.Is(unicode.Latin, character):
			latin++
		}
	}
	return han, latin
}
