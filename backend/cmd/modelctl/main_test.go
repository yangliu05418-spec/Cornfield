package main

import (
	"testing"

	"internal-image-studio/internal/modelconfig"
)

func TestCurrentModelIDsRejectsEmptyCatalog(t *testing.T) {
	if _, err := currentModelIDs(nil); err == nil {
		t.Fatal("currentModelIDs accepted an empty catalog")
	}
}

func TestCurrentModelIDsPreservesCurrentSet(t *testing.T) {
	ids, err := currentModelIDs([]modelconfig.Model{{ID: "legacy-kept"}, {ID: "new-model"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "legacy-kept" || ids[1] != "new-model" {
		t.Fatalf("unexpected model IDs: %#v", ids)
	}
}

func TestCapabilitySnapshotMatchesLegacyPolicyKeysOnly(t *testing.T) {
	legacy := []byte(`{"id":"model","policy":{"GenerationTimeoutSeconds":900}}`)
	current := []byte(`{"policy":{"generation_timeout_seconds":900},"id":"model"}`)
	if !capabilitySnapshotMatches(legacy, current) {
		t.Fatal("legacy policy key representation caused an immutable snapshot conflict")
	}
	changed := []byte(`{"policy":{"generation_timeout_seconds":901},"id":"model"}`)
	if capabilitySnapshotMatches(legacy, changed) {
		t.Fatal("semantic snapshot change was accepted")
	}
}
