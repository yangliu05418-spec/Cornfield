package worker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestAttachedProviderJobNeverUsesSubmitPath(t *testing.T) {
	providerJobID := "remote-job-123"
	attached := generationRecord{Status: "provider_pending", ProviderJobID: &providerJobID}
	if needsProviderSubmission(attached) {
		t.Fatal("an attached provider job must be polled, never submitted again")
	}
	if !needsProviderSubmission(generationRecord{Status: "dispatched"}) {
		t.Fatal("a fresh dispatched job should use the provider submit path")
	}
}

func TestReconciledRiverJobHasDistinctUniqueArgs(t *testing.T) {
	fresh := GenerateArgs{GenerationJobID: "job-123", ExecutionGeneration: 1}
	reconciled := GenerateArgs{GenerationJobID: "job-123", ExecutionGeneration: 2, ReconciledProviderJobID: "remote-456"}
	freshJSON, err := json.Marshal(fresh)
	if err != nil {
		t.Fatal(err)
	}
	reconciledJSON, err := json.Marshal(reconciled)
	if err != nil {
		t.Fatal(err)
	}
	if string(freshJSON) == string(reconciledJSON) || !strings.Contains(string(reconciledJSON), "remote-456") {
		t.Fatalf("reconciled args must differ from the old River attempt: %s / %s", freshJSON, reconciledJSON)
	}
	field, ok := reflect.TypeOf(GenerateArgs{}).FieldByName("ReconciledProviderJobID")
	if !ok || field.Tag.Get("river") != "unique" {
		t.Fatal("reconciliation discriminator must participate in River uniqueness")
	}
	generationField, ok := reflect.TypeOf(GenerateArgs{}).FieldByName("ExecutionGeneration")
	if !ok || generationField.Tag.Get("river") != "unique" {
		t.Fatal("execution generation must participate in River uniqueness")
	}
}

func TestExecutionGenerationKeepsLegacyJobsOnFirstExecution(t *testing.T) {
	if got := normalizeExecutionGeneration(0); got != 1 {
		t.Fatalf("legacy execution generation = %d, want 1", got)
	}
	if got := normalizeExecutionGeneration(2); got != 2 {
		t.Fatalf("current execution generation = %d, want 2", got)
	}
	legacyJSON, err := json.Marshal(GenerateArgs{GenerationJobID: "job-123"})
	if err != nil {
		t.Fatal(err)
	}
	firstExecutionJSON, err := json.Marshal(GenerateArgs{GenerationJobID: "job-123", ExecutionGeneration: riverExecutionGeneration(1)})
	if err != nil {
		t.Fatal(err)
	}
	if string(firstExecutionJSON) != string(legacyJSON) {
		t.Fatalf("first execution changed the legacy unique args: %s / %s", firstExecutionJSON, legacyJSON)
	}
	secondExecutionJSON, err := json.Marshal(GenerateArgs{GenerationJobID: "job-123", ExecutionGeneration: riverExecutionGeneration(2)})
	if err != nil {
		t.Fatal(err)
	}
	if string(secondExecutionJSON) == string(legacyJSON) {
		t.Fatalf("second execution reused legacy unique args: %s", secondExecutionJSON)
	}
}
