package httpapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlanSubmissionReconciliationAttachesOnlyForPolling(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	plan, err := planSubmissionReconciliation("submission_uncertain", reconcileSubmissionInput{
		Action:        "attach_provider_job",
		ProviderJobID: " remote-job-123 ",
	}, now, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProviderJobID == nil || *plan.ProviderJobID != "remote-job-123" {
		t.Fatalf("provider job id = %v", plan.ProviderJobID)
	}
	if plan.NextOperation != "poll_provider" || plan.ResetAttempts || plan.DuplicateCostRisk {
		t.Fatalf("attach plan could submit again: %+v", plan)
	}
	if plan.GenerationDeadline == nil || !plan.GenerationDeadline.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("deadline = %v", plan.GenerationDeadline)
	}
	if plan.UpstreamActiveUntil == nil || !plan.UpstreamActiveUntil.Equal(*plan.GenerationDeadline) {
		t.Fatalf("upstream lease = %v, want %v", plan.UpstreamActiveUntil, plan.GenerationDeadline)
	}
}

func TestPlanSubmissionReconciliationRequiresExplicitRemoteAbsence(t *testing.T) {
	_, err := planSubmissionReconciliation("submission_uncertain", reconcileSubmissionInput{Action: "confirm_absent"}, time.Now(), time.Minute)
	if !errors.Is(err, errRemoteAbsenceUnconfirmed) {
		t.Fatalf("error = %v, want explicit confirmation error", err)
	}
	plan, err := planSubmissionReconciliation("submission_uncertain", reconcileSubmissionInput{
		Action:                "confirm_absent",
		ConfirmedRemoteAbsent: true,
	}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ProviderJobID != nil || plan.UpstreamActiveUntil != nil || !plan.ResetAttempts || !plan.DuplicateCostRisk || plan.NextOperation != "submit_provider" {
		t.Fatalf("confirm_absent plan = %+v", plan)
	}
}

func TestPlanSubmissionReconciliationCanCloseAcceptedUnrecoverableResult(t *testing.T) {
	input := reconcileSubmissionInput{Action: "confirm_accepted_unrecoverable"}
	_, err := planSubmissionReconciliation("submission_uncertain", input, time.Now(), time.Minute)
	if !errors.Is(err, errAcceptedLossUnconfirmed) {
		t.Fatalf("error = %v, want explicit accepted-loss confirmation", err)
	}
	input.ConfirmedProviderAccepted = true
	input.ConfirmedResultUnrecoverable = true
	plan, err := planSubmissionReconciliation("submission_uncertain", input, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "failed" || plan.Retryable || !plan.ProviderChargeAssumed || plan.NextOperation != "none" || plan.DuplicateCostRisk {
		t.Fatalf("accepted-unrecoverable plan = %+v", plan)
	}
}

func TestPlanSubmissionReconciliationRejectsWrongStateAndInvalidProviderID(t *testing.T) {
	_, err := planSubmissionReconciliation("failed", reconcileSubmissionInput{Action: "attach_provider_job", ProviderJobID: "remote-1"}, time.Now(), time.Minute)
	if !errors.Is(err, errReconciliationState) {
		t.Fatalf("error = %v, want state conflict", err)
	}
	for _, value := range []string{"", "has whitespace", strings.Repeat("x", maxProviderJobIDBytes+1)} {
		_, err = planSubmissionReconciliation("submission_uncertain", reconcileSubmissionInput{Action: "attach_provider_job", ProviderJobID: value}, time.Now(), time.Minute)
		if !errors.Is(err, errProviderJobID) {
			t.Fatalf("provider job id %q error = %v", value, err)
		}
	}
	_, err = planSubmissionReconciliation("submission_uncertain", reconcileSubmissionInput{
		Action: "confirm_absent", ProviderJobID: "remote-1", ConfirmedRemoteAbsent: true,
	}, time.Now(), time.Minute)
	if !errors.Is(err, errReconciliationAction) {
		t.Fatalf("contradictory input error = %v", err)
	}
}

func TestReconciliationPollWindowIsBounded(t *testing.T) {
	if got := reconciliationPollWindow(0); got != 15*time.Minute {
		t.Fatalf("fallback poll window = %s", got)
	}
	if got := reconciliationPollWindow(int((48 * time.Hour) / time.Second)); got != maxReconciliationPollWindow {
		t.Fatalf("bounded poll window = %s", got)
	}
}

func TestOnlyAsynchronousProviderCanAttachRemoteJob(t *testing.T) {
	if !providerSupportsPolling("legnext") {
		t.Fatal("Legnext must support reconciliation polling")
	}
	if providerSupportsPolling("openrouter") {
		t.Fatal("OpenRouter's synchronous image API must never enter the poll path")
	}
}

type failingPasswordReader struct{}

func (failingPasswordReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestTemporaryPasswordPropagatesEntropyFailure(t *testing.T) {
	if password, err := temporaryPasswordFrom(failingPasswordReader{}); err == nil || password != "" {
		t.Fatalf("temporaryPasswordFrom() = (%q,%v), want error", password, err)
	}
	password, err := temporaryPasswordFrom(strings.NewReader(strings.Repeat("a", 18)))
	if err != nil || !strings.HasPrefix(password, "T9!") || len(password) != 27 {
		t.Fatalf("temporaryPasswordFrom() = (%q,%v)", password, err)
	}
}
