package worker

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
)

func TestDesiredWorkerBatchStatus(t *testing.T) {
	tests := []struct {
		name   string
		counts workerBatchCounts
		want   string
	}{
		{name: "queued", counts: workerBatchCounts{total: 2}, want: "queued"},
		{name: "running", counts: workerBatchCounts{total: 2, running: 1}, want: "running"},
		{name: "cancelling", counts: workerBatchCounts{total: 2, cancelling: 1, running: 1}, want: "cancelling"},
		{name: "succeeded", counts: workerBatchCounts{total: 2, succeeded: 2}, want: "succeeded"},
		{name: "partial", counts: workerBatchCounts{total: 2, succeeded: 1, failed: 1}, want: "partial"},
		{name: "cancelled", counts: workerBatchCounts{total: 2, cancelled: 2}, want: "cancelled"},
		{name: "failed", counts: workerBatchCounts{total: 2, failed: 1, cancelled: 1}, want: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := desiredWorkerBatchStatus(test.counts); got != test.want {
				t.Fatalf("desiredWorkerBatchStatus() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWorkerBatchWithMissingOutputsIsPartial(t *testing.T) {
	if got := workerBatchStatusWithOutputCount("succeeded", 2, 4); got != "partial" {
		t.Fatalf("workerBatchStatusWithOutputCount() = %q, want partial", got)
	}
	if got := workerBatchStatusWithOutputCount("succeeded", 4, 4); got != "succeeded" {
		t.Fatalf("complete output count changed status to %q", got)
	}
}

func TestSubmissionPolicyAndRetryDelay(t *testing.T) {
	if got := maxSubmissionAttempts(modelconfig.Policy{MaxSafeRetries: 3}); got != 4 {
		t.Fatalf("maxSubmissionAttempts() = %d, want 4", got)
	}
	jobID := uuid.MustParse("4e6679cf-9f03-4c29-91c6-7c66ec6b2611")
	first := safeRetryDelay(jobID, 3)
	second := safeRetryDelay(jobID, 3)
	if first != second {
		t.Fatalf("retry jitter is not deterministic: %s != %s", first, second)
	}
	if first < 0 || first > 4*time.Second {
		t.Fatalf("retry delay %s exceeds attempt ceiling", first)
	}
}

func TestBoundedSnoozeHonorsDeadline(t *testing.T) {
	deadline := time.Now().Add(250 * time.Millisecond)
	got := boundedSnooze(5*time.Second, &deadline)
	if got < 100*time.Millisecond || got > 300*time.Millisecond {
		t.Fatalf("boundedSnooze() = %s, want deadline-bounded duration", got)
	}
}

func TestCancelledUpstreamLeaseOnlyTracksUncancellableRemoteWork(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	deadline := now.Add(10 * time.Minute)
	providerJobID := "remote-123"
	item := generationRecord{
		ProviderJobID:      &providerJobID,
		GenerationDeadline: &deadline,
		ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{
			GenerationTimeoutSeconds: 900,
		}},
	}
	submission := provider.Submission{ProviderJobID: providerJobID}

	got := cancelledUpstreamDeadline(now, item, submission, provider.CancelResult{Accepted: false, Mode: "discard_result_only"}, nil)
	wantDeadline := deadline.Add(15 * time.Minute)
	if got == nil || !got.Equal(wantDeadline) {
		t.Fatalf("uncancellable remote deadline = %v, want %v", got, wantDeadline)
	}
	if got = cancelledUpstreamDeadline(now, item, submission, provider.CancelResult{Accepted: true, Mode: "requested_upstream"}, nil); got != nil {
		t.Fatalf("accepted upstream cancellation retained a lease: %v", got)
	}
	if got = cancelledUpstreamDeadline(now, item, provider.Submission{}, provider.CancelResult{Accepted: false}, errors.New("cancel failed")); got != nil {
		t.Fatalf("local cancellation without a remote id retained a lease: %v", got)
	}
	item.CancelMode = "discard_result_only"
	if got = cancelledUpstreamDeadline(now, item, provider.Submission{}, provider.CancelResult{Accepted: true, Mode: "local"}, nil); got == nil || !got.Equal(wantDeadline) {
		t.Fatalf("interrupted Submit cancellation deadline = %v, want %v", got, wantDeadline)
	}
	past := now.Add(-16 * time.Minute)
	item.GenerationDeadline = &past
	if got = cancelledUpstreamDeadline(now, item, submission, provider.CancelResult{Accepted: false}, errors.New("cancel failed")); got != nil {
		t.Fatalf("expired remote work retained a lease: %v", got)
	}
}

func TestCancelledUpstreamLeaseFallsBackToPersistedModelPolicy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	item := generationRecord{ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{GenerationTimeoutSeconds: 420}}}
	got := cancelledUpstreamDeadline(now, item, provider.Submission{ProviderJobID: "remote-123"}, provider.CancelResult{}, errors.New("cancel failed"))
	if got == nil || !got.Equal(now.Add(420*time.Second)) {
		t.Fatalf("fallback deadline = %v, want %v", got, now.Add(420*time.Second))
	}
}

func TestTimedOutUncancellableProviderRetainsConservativeOccupancy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	item := generationRecord{
		ProviderID:         "legnext",
		GenerationDeadline: &now,
		ModelSnapshot: modelconfig.Model{Policy: modelconfig.Policy{
			GenerationTimeoutSeconds: 900,
		}},
	}
	submission := provider.Submission{ProviderJobID: "remote-123"}
	got := timedOutUpstreamDeadline(now, item, submission, provider.CancelResult{Accepted: false}, nil)
	if got == nil || !got.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("uncancellable timeout lease = %v, want %v", got, now.Add(15*time.Minute))
	}
	if got = timedOutUpstreamDeadline(now, item, submission, provider.CancelResult{Accepted: true}, nil); got != nil {
		t.Fatalf("accepted timeout cancellation retained a lease: %v", got)
	}
	item.ProviderID = "openrouter"
	if got = timedOutUpstreamDeadline(now, item, submission, provider.CancelResult{}, errors.New("cancel unsupported")); got != nil {
		t.Fatalf("completed synchronous provider retained timeout occupancy: %v", got)
	}
}

func TestSynchronousProviderResponseDoesNotRetainRemoteOccupancy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	deadline := now.Add(5 * time.Minute)
	item := generationRecord{ProviderID: "openrouter", GenerationDeadline: &deadline}
	got := cancelledUpstreamDeadline(now, item, provider.Submission{ProviderJobID: "response-123"}, provider.CancelResult{Mode: "discard_result_only"}, nil)
	if got != nil {
		t.Fatalf("completed synchronous provider response retained occupancy: %v", got)
	}
}

func TestResolvedCancelModeUsesAdapterOutcome(t *testing.T) {
	remote := provider.Submission{ProviderJobID: "remote-123"}
	if got := resolvedCancelMode(generationRecord{}, remote, provider.CancelResult{Accepted: true, Mode: "requested_upstream"}, nil); got != "requested_upstream" {
		t.Fatalf("accepted remote cancel mode = %q", got)
	}
	if got := resolvedCancelMode(generationRecord{}, remote, provider.CancelResult{Accepted: false, Mode: "discard_result_only"}, nil); got != "discard_result_only" {
		t.Fatalf("rejected remote cancel mode = %q", got)
	}
	if got := resolvedCancelMode(generationRecord{}, remote, provider.CancelResult{}, errors.New("cancel transport failed")); got != "discard_result_only" {
		t.Fatalf("ambiguous remote cancel mode = %q", got)
	}
	if got := resolvedCancelMode(generationRecord{}, provider.Submission{}, provider.CancelResult{Accepted: true}, nil); got != "local" {
		t.Fatalf("local cancel mode = %q", got)
	}
	if got := resolvedCancelMode(generationRecord{CancelMode: "discard_result_only"}, provider.Submission{}, provider.CancelResult{Accepted: true}, nil); got != "discard_result_only" {
		t.Fatalf("interrupted Submit cancel mode = %q", got)
	}
}

func TestCancelledUncertainSubmissionKeepsConservativeOccupancy(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	deadline := now.Add(5 * time.Minute)
	item := generationRecord{GenerationDeadline: &deadline}
	uncertain := &provider.Error{Code: "SUBMISSION_UNCERTAIN", SubmissionUncertain: true}
	got := uncertainSubmissionDeadline(now, item, uncertain)
	wantDeadline := deadline.Add(15 * time.Minute)
	if got == nil || !got.Equal(wantDeadline) {
		t.Fatalf("uncertain submission deadline = %v, want %v", got, wantDeadline)
	}
	definite := &provider.Error{Code: "PROVIDER_HTTP_429", Retryable: true}
	if got = uncertainSubmissionDeadline(now, item, definite); got != nil {
		t.Fatalf("definitively rejected submission retained occupancy: %v", got)
	}
	if got = uncertainSubmissionDeadline(now, item, errors.New("local validation failed")); got != nil {
		t.Fatalf("local failure retained occupancy: %v", got)
	}
}

func TestSubmissionUncertainStateLeaseExcludesCompletedSyncResult(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	deadline := now.Add(5 * time.Minute)
	item := generationRecord{GenerationDeadline: &deadline}
	got := uncertainStateDeadline(now, item, true)
	wantDeadline := deadline.Add(15 * time.Minute)
	if got == nil || !got.Equal(wantDeadline) {
		t.Fatalf("interrupted submission deadline = %v, want %v", got, wantDeadline)
	}
	if got = uncertainStateDeadline(now, item, false); got != nil {
		t.Fatalf("completed synchronous result retained provider occupancy: %v", got)
	}
}

func TestCancelledUpstreamTerminalStatuses(t *testing.T) {
	for _, status := range []string{"completed", "succeeded", "failed", "cancelled", "canceled", " COMPLETED "} {
		if !providerResultTerminal(status) {
			t.Fatalf("status %q should release upstream occupancy", status)
		}
	}
	for _, status := range []string{"", "queued", "pending", "processing", "running"} {
		if providerResultTerminal(status) {
			t.Fatalf("status %q released upstream occupancy early", status)
		}
	}
}

func TestProviderOutputCountNeverExceedsCapabilitySnapshot(t *testing.T) {
	if !providerOutputCountExceeded(5, 4) {
		t.Fatal("provider output drift above expected count was accepted")
	}
	if providerOutputCountExceeded(4, 4) || providerOutputCountExceeded(2, 4) {
		t.Fatal("valid provider output count was rejected")
	}
}

func TestManualResubmitSafetyAfterProviderAcceptance(t *testing.T) {
	providerJobID := "upstream-123"
	tests := []struct {
		name string
		item generationRecord
		want bool
	}{
		{name: "locally dispatched", item: generationRecord{Status: "dispatched"}, want: true},
		{name: "sync result ingesting without provider id", item: generationRecord{Status: "ingesting"}, want: false},
		{name: "async provider pending", item: generationRecord{Status: "provider_pending", ProviderJobID: &providerJobID}, want: false},
		{name: "asset ingest after async completion", item: generationRecord{Status: "ingesting", ProviderJobID: &providerJobID}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeForManualResubmit(test.item); got != test.want {
				t.Fatalf("safeForManualResubmit() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPersistedBreakerRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	future := now.Add(3 * time.Second)
	if delay, open := persistedBreakerRetryAfter(now, &future); !open || delay != 3*time.Second {
		t.Fatalf("persistedBreakerRetryAfter() = (%s,%v), want (3s,true)", delay, open)
	}
	past := now.Add(-time.Second)
	if delay, open := persistedBreakerRetryAfter(now, &past); open || delay != 0 {
		t.Fatalf("expired breaker returned (%s,%v)", delay, open)
	}
}

func TestDeterministicProviderRejectionsDoNotTripBreaker(t *testing.T) {
	breaker := NewBreaker()
	worker := &GenerateWorker{Breaker: breaker}
	model := modelconfig.Model{Policy: modelconfig.Policy{
		BreakerMinRequests:     10,
		BreakerFailureRatio:    0.5,
		BreakerCooldownSeconds: 30,
	}}
	key := "legnext:midjourney-v7"
	rejection := &provider.Error{
		Code:      "CONTENT_POLICY_REJECTED",
		Telemetry: provider.Telemetry{HTTPStatus: 403},
	}

	for range 10 {
		if !breaker.Allow(key) {
			t.Fatal("deterministic rejection unexpectedly opened the breaker")
		}
		worker.recordBreaker(context.Background(), generationRecord{}, model, key, rejection)
	}
	if !breaker.Allow(key) {
		t.Fatal("content policy rejections opened the shared provider breaker")
	}
}

func TestPassivePolicyRejectionsDoNotTripBreaker(t *testing.T) {
	breaker := NewBreaker()
	worker := &GenerateWorker{Breaker: breaker}
	model := modelconfig.Model{Policy: modelconfig.Policy{
		BreakerMinRequests:     10,
		BreakerFailureRatio:    0.5,
		BreakerCooldownSeconds: 30,
	}}
	key := "legnext:midjourney-v7"
	rejection := &provider.Error{
		Code:      "CONTENT_POLICY_REJECTED",
		Telemetry: provider.Telemetry{HTTPStatus: 403},
	}

	for range 10 {
		worker.recordPassiveBreaker(context.Background(), generationRecord{}, model, key, rejection)
	}
	if !breaker.Allow(key) {
		t.Fatal("passive content policy rejections opened the shared provider breaker")
	}
}

func TestBreakerExemptErrorClassification(t *testing.T) {
	for _, err := range []error{
		&provider.Error{Code: "CONTENT_POLICY_REJECTED"},
		&provider.Error{Code: "UNSUPPORTED_PARAMETER"},
		&provider.Error{Code: "PROVIDER_HTTP_400", Telemetry: provider.Telemetry{HTTPStatus: 400}},
		&provider.Error{Code: "PROVIDER_HTTP_413", Telemetry: provider.Telemetry{HTTPStatus: 413}},
		&provider.Error{Code: "PROVIDER_HTTP_422", Telemetry: provider.Telemetry{HTTPStatus: 422}},
	} {
		if !breakerExemptError(err) {
			t.Fatalf("breakerExemptError(%v) = false", err)
		}
	}
	for _, err := range []error{
		errors.New("transport failed"),
		&provider.Error{Code: "PROVIDER_HTTP_403", PauseProvider: true, Telemetry: provider.Telemetry{HTTPStatus: 403}},
		&provider.Error{Code: "PROVIDER_HTTP_429", Retryable: true, Telemetry: provider.Telemetry{HTTPStatus: 429}},
		&provider.Error{Code: "PROVIDER_HTTP_500", Retryable: true, Telemetry: provider.Telemetry{HTTPStatus: 500}},
	} {
		if breakerExemptError(err) {
			t.Fatalf("breakerExemptError(%v) = true", err)
		}
	}
}

func TestCompletedSubmissionCannotBypassDurableStaging(t *testing.T) {
	worker := &GenerateWorker{}
	accepted, err := worker.acceptSubmission(context.Background(), generationRecord{}, provider.Submission{Completed: true})
	if err == nil || accepted {
		t.Fatalf("acceptSubmission() = (%v,%v), completed results must use durable staging", accepted, err)
	}
}

func TestAttemptUsageDropsArbitraryProviderBodyFields(t *testing.T) {
	usage := sanitizeAttemptUsage(map[string]any{
		"cost":            0.02,
		"prompt_tokens":   float64(12),
		"reference_count": 1,
		"raw_response":    "data:image/png;base64,must-not-survive",
		"api_key":         "sk-must-not-survive",
		"unknown_number":  99,
		"total_tokens":    math.Inf(1),
		"cost_details": map[string]any{
			"upstream_inference_cost": 0.01,
			"provider_message":        "must-not-survive",
		},
	})
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "must-not-survive") || strings.Contains(text, "raw_response") || strings.Contains(text, "api_key") || strings.Contains(text, "unknown_number") {
		t.Fatalf("unsafe usage survived normalization: %s", text)
	}
	if !strings.Contains(text, `"cost":0.02`) || !strings.Contains(text, `"upstream_inference_cost":0.01`) {
		t.Fatalf("expected numeric usage was removed: %s", text)
	}
	if !strings.Contains(text, `"reference_count":1`) {
		t.Fatalf("expected reference count was removed: %s", text)
	}
}

func TestBoundedAttemptMessagePreservesUTF8Boundary(t *testing.T) {
	message := strings.Repeat("田", 500)
	bounded := boundedAttemptMessage(message)
	if len(bounded) > 1024 || !strings.HasSuffix(bounded, "...") {
		t.Fatalf("bounded message length=%d suffix=%q", len(bounded), bounded[len(bounded)-3:])
	}
}

func TestGenerationProtocolUsesImmutableModelSnapshot(t *testing.T) {
	oldModel := modelconfig.Model{
		ID:                "image-model",
		Provider:          "legnext",
		ProviderModel:     "midjourney-v-old",
		PromptSuffix:      "--old-style",
		RequestParameters: []string{"aspect_ratio"},
		OutputsPerDraw:    4,
		Policy: modelconfig.Policy{
			SubmitTimeoutSeconds: 20, GenerationTimeoutSeconds: 600, MaxConcurrency: 2, MaxSafeRetries: 1,
			BreakerMinRequests: 5, BreakerFailureRatio: .4, BreakerCooldownSeconds: 25,
			AllowedOutputHosts: []string{"old.cdn.example"},
		},
	}
	newModel := oldModel
	newModel.Provider = "openrouter"
	newModel.ProviderModel = "new/provider-model"
	newModel.PromptSuffix = "new suffix"
	newModel.RequestParameters = []string{"resolution", "n"}
	newModel.Policy.MaxSafeRetries = 7
	newModel.Policy.AllowedOutputHosts = []string{"new.cdn.example"}
	newModel.Enabled = true
	currentCatalog := &modelconfig.Catalog{Revision: 2, Models: []modelconfig.Model{newModel}}
	if current, ok := currentCatalog.Find(oldModel.ID); !ok || current.ProviderModel == oldModel.ProviderModel {
		t.Fatal("test setup did not switch the current catalog")
	}

	raw, err := json.Marshal(oldModel)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := decodeModelSnapshot(raw, oldModel.ID, oldModel.OutputsPerDraw)
	if err != nil {
		t.Fatal(err)
	}
	item := generationRecord{
		JobID: uuid.MustParse("71f867d1-576d-4e9e-897a-e40549806d72"), Prompt: "a cornfield",
		AspectRatio: "16:9", Resolution: "2K", ExpectedOutputs: snapshot.OutputsPerDraw,
		ProviderID: snapshot.Provider, ModelSnapshot: snapshot,
	}
	request := canonicalRequestFromSnapshot(item)
	if item.ProviderID != "legnext" || request.Model != "midjourney-v-old" || request.Prompt != "a cornfield --old-style" {
		t.Fatalf("request drifted from historical snapshot: provider=%q model=%q prompt=%q", item.ProviderID, request.Model, request.Prompt)
	}
	if len(request.RequestParameters) != 1 || request.RequestParameters[0] != "aspect_ratio" || maxSubmissionAttempts(snapshot.Policy) != 2 {
		t.Fatalf("historical parameters or retry policy changed: %#v, attempts=%d", request.RequestParameters, maxSubmissionAttempts(snapshot.Policy))
	}
	if got := snapshot.Policy.AllowedOutputHosts; len(got) != 1 || got[0] != "old.cdn.example" {
		t.Fatalf("historical output allowlist changed: %#v", got)
	}
}

func TestDecodeModelSnapshotRejectsOutputDrift(t *testing.T) {
	model := modelconfig.Model{
		ID: "image-model", Provider: "openrouter", ProviderModel: "provider/model", OutputsPerDraw: 1,
		Policy: modelconfig.Policy{SubmitTimeoutSeconds: 1, GenerationTimeoutSeconds: 1, MaxConcurrency: 1, BreakerMinRequests: 1, BreakerFailureRatio: 1, BreakerCooldownSeconds: 1},
	}
	raw, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeModelSnapshot(raw, model.ID, 4); err == nil {
		t.Fatal("decodeModelSnapshot accepted a job/snapshot output mismatch")
	}
}
