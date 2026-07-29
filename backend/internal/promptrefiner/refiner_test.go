package promptrefiner

import (
	"strings"
	"testing"
)

func TestEmbeddedRulesLoad(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if engine.version != "2026-07-29.1" || len(engine.rules) < 20 {
		t.Fatalf("unexpected embedded rules: version=%q count=%d", engine.version, len(engine.rules))
	}
	for _, rule := range engine.rules {
		if rule.Source.Type == "community" && (!strings.Contains(rule.Source.Reference, "@") || rule.Source.License == "") {
			t.Fatalf("community rule %s is not pinned and licensed", rule.ID)
		}
	}
}

func TestRefineEnglishChineseAndExceptions(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result := engine.Refine("Blood orange beside a BLOODY coat，远处有血迹")
	if result.Status != "findings" || len(result.Findings) != 2 {
		t.Fatalf("unexpected findings: %#v", result.Findings)
	}
	if result.Findings[0].Original != "BLOODY" || result.Findings[1].Original != "血迹" {
		t.Fatalf("wrong matched text: %#v", result.Findings)
	}
	reconstructed := ""
	for _, segment := range result.Segments {
		reconstructed += segment.Text
	}
	if reconstructed != "Blood orange beside a BLOODY coat，远处有血迹" {
		t.Fatalf("segments changed the prompt: %q", reconstructed)
	}
}

func TestRefineLongestMatchAndWordBoundaries(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result := engine.Refine("bloodbath bloodshot blood")
	if len(result.Findings) != 3 {
		t.Fatalf("expected the three explicit vocabulary entries, got %#v", result.Findings)
	}
	if result.Findings[0].Original != "bloodbath" || result.Findings[1].Original != "bloodshot" || result.Findings[2].Original != "blood" {
		t.Fatalf("unexpected matches: %#v", result.Findings)
	}
}

func TestRefineFullWidthAndManualOnly(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result := engine.Refine("ＰＯＲＮ与乳头")
	if len(result.Findings) != 2 {
		t.Fatalf("expected two findings, got %#v", result.Findings)
	}
	if result.Findings[1].Mode != "manual_only" || len(result.Findings[1].Replacements) != 0 {
		t.Fatalf("manual-only rule unexpectedly has replacements: %#v", result.Findings[1])
	}
}

func TestCleanPrompt(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	result := engine.Refine("一片安静的玉米田，远处有柔和的黄昏")
	if result.Status != "clean" || len(result.Findings) != 0 || len(result.Segments) != 1 {
		t.Fatalf("unexpected clean result: %#v", result)
	}
}

func TestWorkspaceRulesCoverReferenceVocabulary(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatal(err)
	}
	terms := make(map[string]bool)
	for _, rule := range engine.rules {
		if rule.Source.Type != "community" {
			for _, term := range rule.Terms {
				terms[strings.ToLower(term)] = true
			}
		}
	}
	required := []string{
		"bloodshot", "hemoglobin", "cronenberg", "khorne", "disturbing",
		"wearing nothing", "negligee", "pleasures", "slavegirl", "playboy",
		"pinup", "jav", "succubus", "legs spread", "sexualiz", "bodily fluids",
		"mommy milker", "prophet mohammed", "president xi", "lolita",
		"tied up", "rear end", "cocaine", "vomit",
	}
	for _, term := range required {
		if !terms[term] {
			t.Errorf("workspace reference term %q is missing", term)
		}
	}
}
