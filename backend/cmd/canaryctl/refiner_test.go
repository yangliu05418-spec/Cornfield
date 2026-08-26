package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/refinercanary"
)

func TestRefinerProtocolPacingRespectsAPIQuota(t *testing.T) {
	const minimumInterval = time.Minute / 10
	if refinerProtocolInterval < minimumInterval {
		t.Fatalf("protocol interval = %s, want at least %s", refinerProtocolInterval, minimumInterval)
	}
}

func TestRefinerCanaryReportContainsNoPromptMaterial(t *testing.T) {
	fixtures, err := refinercanary.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	report := refinerCanaryReport{
		Mode: "refiner-e2e", ReleaseSHA: "release", CapabilityRevision: "revision",
		RefinerModel: "synthetic/model", StartedAt: time.Unix(1, 0), CompletedAt: time.Unix(2, 0),
	}
	for _, fixture := range refinercanary.E2EFixtures(fixtures) {
		report.Results = append(report.Results, refinerCanaryResult{
			CaseID: fixture.ID, Class: fixture.Class, TargetProvider: fixture.TargetProvider,
			TargetModel: fixture.TargetModel, Status: "passed", SourceRunes: len([]rune(fixture.Original)), ResultRunes: 7,
		})
	}
	path := filepath.Join(t.TempDir(), "refiner-report.json")
	if err = writePrivateJSON(path, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded refinerCanaryReport
	if err = json.Unmarshal(data, &decoded); err != nil || len(decoded.Results) != len(report.Results) {
		t.Fatalf("report round trip failed: results=%d err=%v", len(decoded.Results), err)
	}
	text := strings.ToLower(string(data))
	for _, forbiddenField := range []string{"\"prompt\"", "\"output\"", "\"hash\"", "\"reasoning\"", "\"raw_error\"", "\"key\"", "error_message"} {
		if strings.Contains(text, forbiddenField) {
			t.Fatalf("report contains forbidden field %s", forbiddenField)
		}
	}
	for _, fixture := range fixtures {
		if strings.Contains(string(data), fixture.Original) || fixture.Candidate != "" && strings.Contains(string(data), fixture.Candidate) {
			t.Fatalf("report contains synthetic prompt material from %s", fixture.ID)
		}
	}
}

func TestReadSecretLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("key-a\r\nkey-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := readSecretLines(path)
	if err != nil || len(keys) != 2 || keys[0] != "key-a" || keys[1] != "key-b" {
		t.Fatalf("keys=%v err=%v", keys, err)
	}
	if err := os.WriteFile(path, []byte("key-a\nkey-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readSecretLines(path); err == nil {
		t.Fatal("duplicate key pool was accepted")
	}
}

func TestRefinerE2EPayloadUsesValidCatalogDefaults(t *testing.T) {
	model := modelconfig.Model{
		ID: "legnext-midjourney", Provider: "legnext", ProviderModel: "midjourney", Enabled: true,
		Capabilities: modelconfig.Capabilities{
			AspectRatios: []string{"1:1"}, Resolutions: []string{"SD"}, MidjourneyVersions: []string{"8.2"},
			DrawCount: modelconfig.DrawCount{Min: 1, Max: 1, Default: 1},
		},
	}
	payload := refinerE2EPayload(model, "revision", "synthetic input")
	if payload["aspect_ratio"] != "1:1" || payload["resolution"] != "SD" || payload["draw_count"] != 1 {
		t.Fatalf("payload defaults = %#v", payload)
	}
}

func TestRefinerCanaryProfilesAreBoundedAndPaced(t *testing.T) {
	fixtures, err := refinercanary.Fixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(refinercanary.ProtocolFixtures(fixtures)) != 5 || len(refinercanary.E2EFixtures(fixtures)) != 10 {
		t.Fatal("refiner canary case counts changed without an explicit budget review")
	}
	if refinerProtocolInterval < time.Second || refinerE2EInterval < 6*time.Second {
		t.Fatalf("unsafe refiner pacing: protocol=%s e2e=%s", refinerProtocolInterval, refinerE2EInterval)
	}
}

func TestRefinerE2EDefaultReportPath(t *testing.T) {
	if got := defaultCanaryReportPath("refiner-e2e", "1234567890abcdef"); got != "refiner-e2e-1234567890ab.json" {
		t.Fatalf("refiner report path = %q", got)
	}
	if got := defaultCanaryReportPath("matrix", "1234567890abcdef"); got != "canary-1234567890ab.json" {
		t.Fatalf("matrix report path = %q", got)
	}
}
