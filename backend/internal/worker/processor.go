package worker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/errgroup"

	"internal-image-studio/internal/blob"
	"internal-image-studio/internal/config"
	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
	"internal-image-studio/internal/providercallback"
	"internal-image-studio/internal/providerurl"
)

type GenerateArgs struct {
	GenerationJobID         string `json:"generation_job_id" river:"unique"`
	ExecutionGeneration     int    `json:"execution_generation,omitempty" river:"unique"`
	ReconciledProviderJobID string `json:"reconciled_provider_job_id,omitempty" river:"unique"`
}

func (GenerateArgs) Kind() string { return "generation.execute" }

type GenerateWorker struct {
	river.WorkerDefaults[GenerateArgs]
	DB                 *pgxpool.Pool
	Config             config.Config
	Blobs              *blob.Local
	Adapters           map[string]provider.Adapter
	ProviderSem        map[string]chan struct{}
	IngestSem          chan struct{}
	ThumbSem           chan struct{}
	OptionalThumbQueue chan string
	HTTPClient         *http.Client
	Log                *slog.Logger
	Breaker            *Breaker
}

type generationRecord struct {
	JobID               uuid.UUID
	BatchID             uuid.UUID
	OwnerID             uuid.UUID
	Status              string
	CancelMode          string
	ExecutionGeneration int
	ProviderJobID       *string
	ProviderPollingURL  *string
	ExpectedOutputs     int
	Prompt              string
	AspectRatio         string
	Resolution          string
	ModelID             string
	ProviderID          string
	ModelSnapshot       modelconfig.Model
	Options             provider.GenerationOptions
	AttemptCount        int
	GenerationDeadline  *time.Time
	UpstreamActiveUntil *time.Time
}

type submissionClaim struct {
	Claimed    bool
	RetryAfter time.Duration
	Reason     string
	Attempt    int
	AttemptID  int64
	Deadline   time.Time
}

type stagedGenerationOutput struct {
	OutputIndex int
	StorageKey  string
	SHA256      string
	MediaType   string
	Width       int
	Height      int
	ByteSize    int64
	BlurDataURL string
}

type preparedGenerationOutput struct {
	output    stagedGenerationOutput
	tempPath  string
	extension string
}

type stagedResultError struct {
	code    string
	message string
}

func (e *stagedResultError) Error() string { return e.message }

// Middleware closes the gap between River's infrastructure retry state and
// the user-visible business state if an unexpected error reaches the final
// River attempt.
func (w *GenerateWorker) Middleware(_ *rivertype.JobRow) []rivertype.WorkerMiddleware {
	return []rivertype.WorkerMiddleware{river.WorkerMiddlewareFunc(func(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
		err := doInner(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}
		var snooze *river.JobSnoozeError
		var cancelled *river.JobCancelError
		if errors.As(err, &snooze) || errors.As(err, &cancelled) {
			return err
		}
		var args GenerateArgs
		if json.Unmarshal(job.EncodedArgs, &args) != nil {
			return err
		}
		jobID, parseErr := uuid.Parse(args.GenerationJobID)
		if parseErr != nil {
			return err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			if persistErr := w.persistSubmissionUncertain(jobID, "SUBMISSION_UNCERTAIN", "Provider submission exceeded its execution deadline; automatic resubmission is disabled", true); persistErr != nil {
				if w.Log != nil {
					w.Log.Error("timed-out submission could not be persisted", "generation_job_id", jobID, "error", persistErr)
				}
				return err
			}
			return river.JobCancel(err)
		}
		if job.Attempt < job.MaxAttempts {
			return err
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		record, loadErr := w.load(persistCtx, jobID)
		if loadErr != nil {
			return err
		}
		if normalizeExecutionGeneration(args.ExecutionGeneration) != record.ExecutionGeneration {
			return river.JobCancel(err)
		}
		if failErr := w.fail(persistCtx, record, "WORKER_RETRIES_EXHAUSTED", "任务执行重试已耗尽", safeForManualResubmit(record)); failErr != nil {
			return err
		}
		return river.JobCancel(err)
	})}
}

func (w *GenerateWorker) Work(ctx context.Context, riverJob *river.Job[GenerateArgs]) error {
	jobID, err := uuid.Parse(riverJob.Args.GenerationJobID)
	if err != nil {
		return nil
	}
	record, err := w.load(ctx, jobID)
	if err != nil {
		return err
	}
	if normalizeExecutionGeneration(riverJob.Args.ExecutionGeneration) != record.ExecutionGeneration {
		return nil
	}
	switch record.Status {
	case "cancelled":
		if !upstreamLeaseActive(time.Now(), record.UpstreamActiveUntil) {
			if record.UpstreamActiveUntil != nil {
				if err := w.clearUpstreamLease(ctx, record.JobID); err != nil {
					return err
				}
			}
			return nil
		}
		adapter := w.Adapters[record.ProviderID]
		if adapter == nil {
			return river.JobSnooze(boundedSnooze(30*time.Second, record.UpstreamActiveUntil))
		}
		return w.observeCancelledUpstream(ctx, record, adapter)
	case "succeeded":
		return nil
	case "failed", "submission_uncertain":
		if !upstreamLeaseActive(time.Now(), record.UpstreamActiveUntil) {
			if record.UpstreamActiveUntil != nil {
				if err := w.clearUpstreamLease(ctx, record.JobID); err != nil {
					return err
				}
			}
			return nil
		}
		adapter := w.Adapters[record.ProviderID]
		if adapter == nil {
			return river.JobSnooze(boundedSnooze(30*time.Second, record.UpstreamActiveUntil))
		}
		return w.observeTerminalUpstream(ctx, record, adapter)
	case "ingesting":
		stagedCount, countErr := w.stagedOutputCount(ctx, record.JobID)
		if countErr != nil {
			return countErr
		}
		if stagedCount > 0 {
			return w.ingestStaged(ctx, record)
		}
		if record.ProviderJobID == nil {
			return w.fail(ctx, record, "STAGED_RESULT_MISSING", "已接收的同步生成结果缺少可恢复暂存数据", false)
		}
	}
	model := record.ModelSnapshot
	adapter := w.Adapters[record.ProviderID]
	if adapter == nil {
		return w.fail(ctx, record, "PROVIDER_NOT_CONFIGURED", "生成服务未配置", false)
	}

	switch record.Status {
	case "cancelling":
		tracked, err := w.completeCancellation(ctx, record, adapter, submissionFromRecord(record), false)
		if err != nil {
			return err
		}
		if tracked {
			return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
		}
		return river.JobCancel(nil)
	case "submitting":
		if record.ProviderJobID == nil {
			return w.persistSubmissionUncertain(record.JobID, "SUBMISSION_UNCERTAIN", "Provider submission was interrupted; automatic resubmission is disabled", true)
		}
	}

	if record.GenerationDeadline != nil && !time.Now().Before(*record.GenerationDeadline) {
		if record.ProviderJobID != nil {
			submission := submissionFromRecord(record)
			// A task can complete just before its deadline while the Worker is
			// restarting. Perform one authenticated final read before declaring a
			// paid result lost; the ordinary deadline-bounded poll would already be
			// cancelled at this point.
			release, acquireErr := w.acquireProvider(ctx, record.ProviderID)
			if acquireErr != nil {
				return acquireErr
			}
			attemptID, attemptErr := w.beginAttempt(ctx, record, "deadline_poll")
			if attemptErr != nil {
				release()
				return attemptErr
			}
			finalPollCtx, cancelFinalPoll := context.WithTimeout(ctx, 45*time.Second)
			started := time.Now()
			finalResult, finalPollErr := adapter.Poll(finalPollCtx, submission)
			cancelFinalPoll()
			release()
			breakerKey := record.ProviderID + ":" + record.ModelID
			finalObservedErr := observedResultError(finalResult, finalPollErr)
			if finalPollErr != nil || terminalProviderResult(finalResult.Status) {
				w.recordPassiveBreaker(ctx, record, model, breakerKey, finalObservedErr)
			}
			w.finishAttempt(attemptID, record, "deadline_poll", time.Since(started), finalObservedErr, finalResult.Usage, finalResult.Telemetry)
			var finalProviderErr *provider.Error
			if errors.As(finalPollErr, &finalProviderErr) && finalProviderErr.PauseProvider {
				if pauseErr := w.pauseProvider(ctx, record, model, finalProviderErr); pauseErr != nil {
					return pauseErr
				}
			}
			if finalPollErr == nil {
				switch strings.ToLower(strings.TrimSpace(finalResult.Status)) {
				case "completed", "succeeded":
					ready, transitionErr := w.transitionToIngesting(ctx, record)
					if transitionErr != nil {
						return transitionErr
					}
					if !ready {
						if err := w.markCancelled(ctx, record, true); err != nil {
							return err
						}
						return river.JobCancel(nil)
					}
					staged, stageErr := w.stageResult(ctx, record, finalResult, valueOrPointer(record.ProviderJobID))
					if stageErr != nil {
						return stageErr
					}
					if !staged {
						return river.JobCancel(nil)
					}
					return w.ingestStaged(ctx, record)
				case "failed":
					return w.fail(ctx, record, valueOr(finalResult.ErrorCode, "PROVIDER_JOB_FAILED"), valueOr(finalResult.ErrorText, "上游生成失败"), finalResult.ErrorRetryable)
				}
			}
			cancelResult, cancelErr := w.cancelRemote(ctx, record, adapter, submission)
			if cancelErr != nil && w.Log != nil {
				w.Log.Warn("timed-out provider job could not be cancelled", "generation_job_id", record.JobID, "error", cancelErr)
			}
			upstreamActiveUntil := timedOutUpstreamDeadline(time.Now(), record, submission, cancelResult, cancelErr)
			if err := w.failWithUpstreamLease(ctx, record, "PROVIDER_TIMEOUT", "生成任务已超过最长等待时间", safeForManualResubmit(record), upstreamActiveUntil); err != nil {
				return err
			}
			if upstreamActiveUntil != nil {
				return river.JobSnooze(boundedSnooze(3*time.Second, upstreamActiveUntil))
			}
			return nil
		}
		return w.fail(ctx, record, "PROVIDER_TIMEOUT", "生成任务已超过最长等待时间", safeForManualResubmit(record))
	}

	breakerKey := record.ProviderID + ":" + record.ModelID
	if needsProviderSubmission(record) {
		if record.AttemptCount >= maxSubmissionAttempts(model.Policy) {
			return w.fail(ctx, record, "SUBMIT_RETRIES_EXHAUSTED", "上游提交重试已耗尽", true)
		}
		// An open circuit stops new paid submissions, but must never stop us
		// polling a job that the provider has already accepted.
		breakerPermit := false
		if w.Breaker != nil && !w.Breaker.Allow(breakerKey) {
			return river.JobSnooze(30 * time.Second)
		}
		if w.Breaker != nil {
			breakerPermit = true
		}
		defer func() {
			if breakerPermit {
				w.Breaker.Abandon(breakerKey)
			}
		}()
		claim, claimErr := w.claimSubmissionSlot(ctx, record, model)
		if claimErr != nil {
			return claimErr
		}
		if !claim.Claimed {
			if claim.Reason == "state_changed" {
				return river.JobSnooze(100 * time.Millisecond)
			}
			return river.JobSnooze(claim.RetryAfter)
		}
		record.Status = "submitting"
		record.AttemptCount = claim.Attempt
		record.GenerationDeadline = &claim.Deadline

		release, acquireErr := w.acquireProvider(ctx, record.ProviderID)
		if acquireErr != nil {
			return acquireErr
		}
		request, requestErr := w.canonicalRequest(ctx, record)
		if requestErr != nil {
			release()
			return w.fail(ctx, record, "REFERENCE_READ_FAILED", requestErr.Error(), false)
		}
		submitCtx, cancelSubmit := boundedContext(ctx, time.Duration(model.Policy.SubmitTimeoutSeconds)*time.Second, record.GenerationDeadline)
		started := time.Now()
		submission, submitErr := adapter.Submit(submitCtx, request)
		cancelSubmit()
		release()
		w.recordBreaker(ctx, record, model, breakerKey, submitErr)
		breakerPermit = false
		var submitUsage map[string]any
		if submission.Completed {
			submitUsage = submission.Result.Usage
		}
		w.finishAttempt(claim.AttemptID, record, "submit", time.Since(started), submitErr, submitUsage, submission.Telemetry)
		if submitErr != nil {
			status, _ := w.currentStatus(ctx, record.JobID)
			if status == "cancelled" || status == "cancelling" {
				deadline := uncertainSubmissionDeadline(time.Now(), record, submitErr)
				if err := w.markCancelledWithUpstreamLease(ctx, record, false, deadline, nil); err != nil {
					return err
				}
				if deadline != nil {
					return river.JobSnooze(boundedSnooze(30*time.Second, deadline))
				}
				return river.JobCancel(nil)
			}
			return w.handleProviderError(ctx, record, model, submitErr, true)
		}
		if submission.Completed {
			staged, stageErr := w.stageResult(ctx, record, submission.Result, submission.ProviderJobID)
			if stageErr != nil {
				var invalid *stagedResultError
				if errors.As(stageErr, &invalid) {
					return w.fail(ctx, record, invalid.code, invalid.message, false)
				}
				if ctx.Err() != nil {
					return stageErr
				}
				// A commit can succeed server-side while its acknowledgement is
				// lost. Re-read durable state before declaring the paid result
				// uncertain so an ambiguous commit still resumes ingestion.
				recoveryCtx, cancelRecovery := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				recovered, loadErr := w.load(recoveryCtx, record.JobID)
				if loadErr == nil && recovered.Status == "ingesting" {
					if stagedCount, countErr := w.stagedOutputCount(recoveryCtx, record.JobID); countErr == nil && stagedCount > 0 {
						cancelRecovery()
						return w.ingestStaged(ctx, recovered)
					}
				}
				cancelRecovery()
				w.Log.Error("synchronous provider result could not be staged", "generation_job_id", record.JobID, "error", stageErr)
				if persistErr := w.persistSubmissionUncertain(record.JobID, "RESULT_STAGING_FAILED", "同步生成已完成，但结果无法可靠暂存；系统不会自动重新提交", false); persistErr != nil {
					return errors.Join(stageErr, persistErr)
				}
				return river.JobCancel(stageErr)
			}
			if !staged {
				if err := w.markCancelled(ctx, record, true); err != nil {
					return err
				}
				return river.JobCancel(nil)
			}
			return w.ingestStaged(ctx, record)
		}

		accepted, acceptErr := w.acceptSubmission(ctx, record, submission)
		if acceptErr != nil {
			return acceptErr
		}
		if !accepted {
			if submission.ProviderJobID != "" {
				record.ProviderJobID = &submission.ProviderJobID
			}
			tracked, cancelErr := w.completeCancellation(ctx, record, adapter, submission, true)
			if cancelErr != nil {
				return cancelErr
			}
			if tracked {
				return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
			}
			return river.JobCancel(nil)
		}
		if submission.ProviderJobID != "" {
			record.ProviderJobID = &submission.ProviderJobID
		}
		return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
	}

	release, acquireErr := w.acquireProvider(ctx, record.ProviderID)
	if acquireErr != nil {
		return acquireErr
	}
	attemptID, attemptErr := w.beginAttempt(ctx, record, "poll")
	if attemptErr != nil {
		release()
		return attemptErr
	}
	pollCtx, cancelPoll := boundedContext(ctx, 45*time.Second, record.GenerationDeadline)
	started := time.Now()
	result, pollErr := adapter.Poll(pollCtx, submissionFromRecord(record))
	cancelPoll()
	release()
	observedErr := observedResultError(result, pollErr)
	if pollErr != nil || terminalProviderResult(result.Status) {
		w.recordPassiveBreaker(ctx, record, model, breakerKey, observedErr)
	}
	w.finishAttempt(attemptID, record, "poll", time.Since(started), observedErr, result.Usage, result.Telemetry)
	if pollErr != nil {
		if status, statusErr := w.currentStatus(ctx, record.JobID); statusErr == nil && (status == "cancelling" || status == "cancelled") {
			tracked, cancelErr := w.completeCancellation(ctx, record, adapter, submissionFromRecord(record), false)
			if cancelErr != nil {
				return cancelErr
			}
			if tracked {
				return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
			}
			return river.JobCancel(nil)
		}
		return w.handleProviderError(ctx, record, model, pollErr, false)
	}
	switch result.Status {
	case "completed", "succeeded":
		ready, transitionErr := w.transitionToIngesting(ctx, record)
		if transitionErr != nil {
			return transitionErr
		}
		if !ready {
			if err := w.markCancelled(ctx, record, true); err != nil {
				return err
			}
			return river.JobCancel(nil)
		}
		staged, stageErr := w.stageResult(ctx, record, result, valueOrPointer(record.ProviderJobID))
		if stageErr != nil {
			var invalid *stagedResultError
			if errors.As(stageErr, &invalid) {
				return w.fail(ctx, record, invalid.code, invalid.message, false)
			}
			return stageErr
		}
		if !staged {
			if err := w.markCancelled(ctx, record, true); err != nil {
				return err
			}
			return river.JobCancel(nil)
		}
		return w.ingestStaged(ctx, record)
	case "failed":
		return w.fail(ctx, record, valueOr(result.ErrorCode, "PROVIDER_JOB_FAILED"), valueOr(result.ErrorText, "上游生成失败"), result.ErrorRetryable)
	default:
		return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
	}
}

func terminalProviderResult(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "failed":
		return true
	default:
		return false
	}
}

func observedResultError(result provider.Result, transportErr error) error {
	if transportErr != nil || strings.ToLower(strings.TrimSpace(result.Status)) != "failed" {
		return transportErr
	}
	return &provider.Error{
		Code:      valueOr(result.ErrorCode, "PROVIDER_JOB_FAILED"),
		Message:   valueOr(result.ErrorText, "provider generation failed"),
		Retryable: result.ErrorRetryable,
		Telemetry: result.Telemetry,
	}
}

func (w *GenerateWorker) load(ctx context.Context, jobID uuid.UUID) (generationRecord, error) {
	var item generationRecord
	var snapshotJSON []byte
	var storedExpectedOutputs int
	err := w.DB.QueryRow(ctx, `SELECT j.id,j.batch_id,j.owner_user_id,j.status,COALESCE(j.cancel_mode,''),j.execution_generation,j.provider_job_id,j.provider_poll_url,j.expected_outputs,
		b.prompt,b.aspect_ratio,b.resolution,b.model_id,j.attempt_count,j.generation_deadline,j.upstream_active_until,v.config,b.options
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.id=$1`, jobID).Scan(
		&item.JobID, &item.BatchID, &item.OwnerID, &item.Status, &item.CancelMode, &item.ExecutionGeneration, &item.ProviderJobID, &item.ProviderPollingURL, &storedExpectedOutputs,
		&item.Prompt, &item.AspectRatio, &item.Resolution, &item.ModelID, &item.AttemptCount, &item.GenerationDeadline, &item.UpstreamActiveUntil, &snapshotJSON, &item.Options)
	if err != nil {
		return item, err
	}
	model, err := decodeModelSnapshot(snapshotJSON, item.ModelID, storedExpectedOutputs)
	if err != nil {
		return item, err
	}
	item.ModelSnapshot = model
	item.ProviderID = model.Provider
	item.ExpectedOutputs = model.OutputsPerDraw
	return item, nil
}

func decodeModelSnapshot(raw []byte, modelID string, storedExpectedOutputs int) (modelconfig.Model, error) {
	var model modelconfig.Model
	if len(raw) == 0 {
		return model, errors.New("model capability snapshot is missing")
	}
	if err := json.Unmarshal(raw, &model); err != nil {
		return model, fmt.Errorf("decode model capability snapshot: %w", err)
	}
	if model.ID != modelID || model.Provider == "" || model.ProviderModel == "" {
		return model, errors.New("model capability snapshot identity is invalid")
	}
	if model.OutputsPerDraw < 1 || model.OutputsPerDraw > 16 || model.OutputsPerDraw != storedExpectedOutputs {
		return model, errors.New("model capability snapshot output count does not match generation job")
	}
	policy := model.Policy
	if policy.SubmitTimeoutSeconds < 1 || policy.GenerationTimeoutSeconds < 1 || policy.MaxConcurrency < 1 || policy.MaxSafeRetries < 0 || policy.BreakerMinRequests < 1 || policy.BreakerFailureRatio <= 0 || policy.BreakerFailureRatio > 1 || policy.BreakerCooldownSeconds < 1 {
		return model, errors.New("model capability snapshot policy is invalid")
	}
	return model, nil
}

func (w *GenerateWorker) claimSubmissionSlot(ctx context.Context, item generationRecord, model modelconfig.Model) (submissionClaim, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return submissionClaim{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "provider-slot:"+item.ProviderID); err != nil {
		return submissionClaim{}, err
	}
	var enabled bool
	var providerState string
	var breakerOpenUntil *time.Time
	if err = tx.QueryRow(ctx, `SELECT enabled,state,breaker_open_until FROM providers WHERE id=$1 FOR UPDATE`, item.ProviderID).Scan(&enabled, &providerState, &breakerOpenUntil); err != nil {
		return submissionClaim{}, err
	}
	if !enabled || providerState == "paused" {
		return submissionClaim{RetryAfter: 30 * time.Second, Reason: "provider_paused"}, nil
	}
	if retryAfter, open := persistedBreakerRetryAfter(time.Now(), breakerOpenUntil); open {
		return submissionClaim{RetryAfter: retryAfter, Reason: "provider_breaker_open"}, nil
	}
	providerSem := w.ProviderSem[item.ProviderID]
	if providerSem == nil || cap(providerSem) < 1 {
		return submissionClaim{}, fmt.Errorf("provider %s has no concurrency limit", item.ProviderID)
	}
	providerLimit := cap(providerSem)
	var providerActive int
	if err = tx.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE v.config->>'provider'=$1)::int
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.status IN ('submitting','provider_pending','cancelling') OR j.upstream_active_until>now()`, item.ProviderID).Scan(&providerActive); err != nil {
		return submissionClaim{}, err
	}
	if providerActive >= providerLimit {
		return submissionClaim{RetryAfter: time.Second, Reason: "quota"}, nil
	}
	seconds := model.Policy.GenerationTimeoutSeconds
	if seconds < 1 {
		seconds = 900
	}
	deadline := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
	var attempt int
	var storedDeadline time.Time
	err = tx.QueryRow(ctx, `UPDATE generation_jobs SET status='submitting',started_at=COALESCE(started_at,now()),
		attempt_count=attempt_count+1,generation_deadline=COALESCE(generation_deadline,$2),updated_at=now()
		WHERE id=$1 AND provider_job_id IS NULL AND status='dispatched'
		RETURNING attempt_count,generation_deadline`, item.JobID, deadline).Scan(&attempt, &storedDeadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return submissionClaim{RetryAfter: 100 * time.Millisecond, Reason: "state_changed"}, nil
	}
	if err != nil {
		return submissionClaim{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_batches SET status='running',updated_at=now() WHERE id=$1 AND status='queued'`, item.BatchID); err != nil {
		return submissionClaim{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.submitting',jsonb_build_object('status','submitting','attempt',$4::integer))`, item.OwnerID, item.BatchID, item.JobID, attempt); err != nil {
		return submissionClaim{}, err
	}
	var attemptID int64
	if err = tx.QueryRow(ctx, `INSERT INTO provider_attempts(job_id,provider_id,operation,attempt_no,outcome)
		VALUES($1,$2,'submit',$3,'started') RETURNING id`, item.JobID, item.ProviderID, attempt).Scan(&attemptID); err != nil {
		return submissionClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return submissionClaim{}, err
	}
	return submissionClaim{Claimed: true, Attempt: attempt, AttemptID: attemptID, Deadline: storedDeadline}, nil
}

func maxSubmissionAttempts(policy modelconfig.Policy) int {
	retries := policy.MaxSafeRetries
	if retries < 0 {
		retries = 0
	}
	return retries + 1
}

func persistedBreakerRetryAfter(now time.Time, openUntil *time.Time) (time.Duration, bool) {
	if openUntil == nil || !now.Before(*openUntil) {
		return 0, false
	}
	delay := openUntil.Sub(now)
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	return delay, true
}

func safeForManualResubmit(item generationRecord) bool {
	// Once an upstream may have accepted the request, a fresh submission can
	// duplicate cost. Only a job that is still locally dispatched is safe.
	return item.Status == "dispatched" && item.ProviderJobID == nil
}

func needsProviderSubmission(item generationRecord) bool {
	return item.ProviderJobID == nil
}

func submissionFromRecord(item generationRecord) provider.Submission {
	return provider.Submission{
		ProviderJobID: valueOrPointer(item.ProviderJobID),
		PollingURL:    valueOrPointer(item.ProviderPollingURL),
	}
}

// stageResult durably associates already-paid provider output with the business
// job. Both synchronous and polled providers use this path, so River retries can
// only resume ingestion and can never create a second upstream generation.
func (w *GenerateWorker) stageResult(ctx context.Context, item generationRecord, result provider.Result, providerJobID string) (bool, error) {
	images := result.Images
	if len(images) == 0 {
		return false, &stagedResultError{code: "PROVIDER_EMPTY_RESULT", message: "上游没有返回图片"}
	}
	if providerOutputCountExceeded(len(images), item.ExpectedOutputs) {
		return false, &stagedResultError{code: "PROVIDER_OUTPUT_COUNT_INVALID", message: "上游返回的图片数量超过模型协议"}
	}
	pending, prepareErr := w.prepareGenerationOutputs(ctx, item, images)
	if prepareErr != nil {
		return false, prepareErr
	}
	defer func() {
		for _, candidate := range pending {
			if candidate.tempPath != "" {
				_ = os.Remove(candidate.tempPath)
			}
		}
	}()
	contentLease := w.Blobs.AcquireContentLease()
	defer contentLease.Release()
	prepared := make([]stagedGenerationOutput, 0, len(pending))
	for index := range pending {
		candidate := &pending[index]
		key, digest, size, err := contentLease.PutImmutable(candidate.tempPath, candidate.extension)
		if err != nil {
			return false, err
		}
		candidate.tempPath = ""
		candidate.output.StorageKey = key
		candidate.output.SHA256 = digest
		candidate.output.ByteSize = size
		prepared = append(prepared, candidate.output)
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&status); err != nil {
		return false, err
	}
	if isBusinessTerminal(status) || status == "cancelling" {
		return false, tx.Commit(ctx)
	}
	if status != "submitting" && status != "ingesting" {
		return false, fmt.Errorf("cannot stage synchronous result in status %s", status)
	}
	for _, output := range prepared {
		if _, err = tx.Exec(ctx, `INSERT INTO generation_staged_outputs(job_id,output_index,storage_key,sha256,media_type,width,height,byte_size,blur_data_url)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (job_id,output_index) DO UPDATE SET blur_data_url=excluded.blur_data_url`, item.JobID, output.OutputIndex, output.StorageKey, output.SHA256, output.MediaType, output.Width, output.Height, output.ByteSize, output.BlurDataURL); err != nil {
			return false, err
		}
	}
	var stagedCount int
	if err = tx.QueryRow(ctx, `SELECT count(*)::int FROM generation_staged_outputs WHERE job_id=$1`, item.JobID).Scan(&stagedCount); err != nil {
		return false, err
	}
	if stagedCount != len(prepared) {
		return false, fmt.Errorf("staged output count %d does not match response count %d", stagedCount, len(prepared))
	}
	if status == "submitting" {
		if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET provider_job_id=COALESCE(provider_job_id,NULLIF($2,'')),status='ingesting',updated_at=now()
			WHERE id=$1 AND status='submitting'`, item.JobID, providerJobID); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
			VALUES($1,$2,$3,'job.updated',jsonb_build_object('status','ingesting','result_staged',true))`, item.OwnerID, item.BatchID, item.JobID); err != nil {
			return false, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (w *GenerateWorker) prepareGenerationOutputs(ctx context.Context, item generationRecord, images []provider.Image) ([]preparedGenerationOutput, error) {
	pending := make([]preparedGenerationOutput, len(images))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(min(4, len(images)))
	for index, image := range images {
		index, image := index, image
		group.Go(func() error {
			candidate, err := w.prepareGenerationOutput(groupCtx, item, index, image)
			if err != nil {
				return err
			}
			pending[index] = candidate
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		for _, candidate := range pending {
			if candidate.tempPath != "" {
				_ = os.Remove(candidate.tempPath)
			}
		}
		return nil, err
	}
	return pending, nil
}

func (w *GenerateWorker) prepareGenerationOutput(ctx context.Context, item generationRecord, index int, output provider.Image) (preparedGenerationOutput, error) {
	tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", fmt.Sprintf("stage-%s-%d-%s.part", item.JobID, index, uuid.NewString()))
	var err error
	if len(output.Bytes) > 0 {
		err = writeSynced(tempPath, output.Bytes)
	} else if output.URL != "" {
		operation := fmt.Sprintf("download_%d", index)
		var attemptID int64
		if w.DB != nil {
			attemptID, err = w.beginAttempt(ctx, item, operation)
			if err != nil {
				return preparedGenerationOutput{}, err
			}
		}
		started := time.Now()
		err = w.downloadToFile(ctx, item, output.URL, tempPath)
		if attemptID != 0 {
			w.finishAttempt(attemptID, item, operation, time.Since(started), err, nil, provider.Telemetry{})
		}
	} else {
		err = errors.New("provider output has no image data")
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return preparedGenerationOutput{}, err
	}
	media, extension, width, height, err := validateProviderImageFile(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return preparedGenerationOutput{}, &stagedResultError{code: "PROVIDER_IMAGE_INVALID", message: err.Error()}
	}
	return preparedGenerationOutput{
		tempPath:  tempPath,
		extension: extension,
		output:    stagedGenerationOutput{OutputIndex: index, MediaType: media, Width: width, Height: height},
	}, nil
}

func (w *GenerateWorker) stagedOutputCount(ctx context.Context, jobID uuid.UUID) (int, error) {
	var count int
	err := w.DB.QueryRow(ctx, `SELECT count(*)::int FROM generation_staged_outputs WHERE job_id=$1`, jobID).Scan(&count)
	return count, err
}

func (w *GenerateWorker) loadStagedOutputs(ctx context.Context, jobID uuid.UUID) ([]stagedGenerationOutput, error) {
	rows, err := w.DB.Query(ctx, `SELECT output_index,storage_key,sha256,media_type,width,height,byte_size,COALESCE(blur_data_url,'')
		FROM generation_staged_outputs WHERE job_id=$1 ORDER BY output_index`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outputs := make([]stagedGenerationOutput, 0)
	for rows.Next() {
		var output stagedGenerationOutput
		if err = rows.Scan(&output.OutputIndex, &output.StorageKey, &output.SHA256, &output.MediaType, &output.Width, &output.Height, &output.ByteSize, &output.BlurDataURL); err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, rows.Err()
}

func (w *GenerateWorker) acceptSubmission(ctx context.Context, item generationRecord, submission provider.Submission) (bool, error) {
	if submission.Completed {
		return false, errors.New("completed provider submissions must be durably staged")
	}
	status := "provider_pending"
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	command, err := tx.Exec(ctx, `UPDATE generation_jobs SET provider_job_id=NULLIF($2,''),provider_poll_url=NULLIF($3,''),status=$4,updated_at=now()
		WHERE id=$1 AND status='submitting' AND provider_job_id IS NULL`, item.JobID, submission.ProviderJobID, submission.PollingURL, status)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.updated',jsonb_build_object('status',$4::text))`, item.OwnerID, item.BatchID, item.JobID, status); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (w *GenerateWorker) transitionToIngesting(ctx context.Context, item generationRecord) (bool, error) {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var current string
	if err = tx.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&current); err != nil {
		return false, err
	}
	if current == "ingesting" {
		return true, tx.Commit(ctx)
	}
	if current != "provider_pending" {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='ingesting',updated_at=now() WHERE id=$1`, item.JobID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.updated',jsonb_build_object('status','ingesting'))`, item.OwnerID, item.BatchID, item.JobID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (w *GenerateWorker) canonicalRequest(ctx context.Context, item generationRecord) (provider.CanonicalRequest, error) {
	request := canonicalRequestFromSnapshot(item)
	prompt := request.Prompt
	if item.ProviderID == "legnext" && w.Config.ProviderCallbackSecret != "" {
		callbackURL, err := providercallback.URL(w.Config.PublicURL, w.Config.ProviderCallbackSecret, item.JobID)
		if err != nil {
			return request, err
		}
		request.CallbackURL = callbackURL
	}
	rows, err := w.DB.Query(ctx, `SELECT a.id,a.storage_key,
		(a.purged_at IS NULL AND a.purge_pending=false AND a.expires_at>now()) AS usable
		FROM generation_input_assets i JOIN assets a ON a.id=i.asset_id
		WHERE i.batch_id=$1 ORDER BY i.position`, item.BatchID)
	if err != nil {
		return request, err
	}
	defer rows.Close()
	for rows.Next() {
		var assetID uuid.UUID
		var key string
		var usable bool
		if err := rows.Scan(&assetID, &key, &usable); err != nil {
			return request, err
		}
		if !usable {
			return request, errors.New("generation reference is unavailable")
		}
		referenceURL, err := providerurl.Sign(w.Config.PublicURL, w.Config.ProviderURLSigningSecret, assetID, filepath.Ext(key), time.Now().Add(time.Hour))
		if err != nil {
			return request, err
		}
		request.ReferenceURLs = append(request.ReferenceURLs, referenceURL)
	}
	if err := rows.Err(); err != nil {
		return request, err
	}
	providerPromptLength := utf8.RuneCountInString(prompt)
	if item.ProviderID == "legnext" {
		providerPromptLength += utf8.RuneCountInString(" --ar ") + utf8.RuneCountInString(item.AspectRatio)
		for _, referenceURL := range request.ReferenceURLs {
			providerPromptLength += utf8.RuneCountInString(referenceURL) + 1
		}
	}
	if providerPromptLength > 8192 {
		return provider.CanonicalRequest{}, errors.New("final provider prompt exceeds 8192 characters")
	}
	return request, nil
}

func canonicalRequestFromSnapshot(item generationRecord) provider.CanonicalRequest {
	model := item.ModelSnapshot
	prompt := item.Prompt
	if model.PromptSuffix != "" {
		prompt += " " + model.PromptSuffix
	}
	var explicitSize string
	if overrides := model.SizeOverrides[item.Resolution]; overrides != nil {
		explicitSize = overrides[item.AspectRatio]
	}
	return provider.CanonicalRequest{
		JobID:             item.JobID.String(),
		Model:             model.ProviderModel,
		Prompt:            prompt,
		AspectRatio:       item.AspectRatio,
		PromptAspectRatio: model.PromptAspectRatio,
		Resolution:        item.Resolution,
		Size:              explicitSize,
		ExpectedImages:    model.OutputsPerDraw,
		RequestParameters: append([]string(nil), model.RequestParameters...),
		Options:           item.Options,
	}
}

func (w *GenerateWorker) handleProviderError(ctx context.Context, item generationRecord, model modelconfig.Model, err error, duringSubmit bool) error {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		if duringSubmit {
			return w.persistSubmissionUncertain(item.JobID, "SUBMISSION_UNCERTAIN", "无法确认上游是否已接收任务，已停止自动重试", true)
		}
		return err
	}
	if providerErr.PauseProvider {
		if pauseErr := w.pauseProvider(ctx, item, model, providerErr); pauseErr != nil {
			return pauseErr
		}
	}
	if providerErr.SubmissionUncertain {
		return w.persistSubmissionUncertain(item.JobID, "SUBMISSION_UNCERTAIN", "提交结果不确定，已停止自动重试", true)
	}
	if !providerErr.Retryable {
		return w.fail(ctx, item, providerErr.Code, providerErr.Message, false)
	}
	delay := providerErr.RetryAfter
	if delay <= 0 {
		delay = safeRetryDelay(item.JobID, item.AttemptCount)
	}
	if !duringSubmit {
		return river.JobSnooze(boundedSnooze(delay, item.GenerationDeadline))
	}
	if item.AttemptCount >= maxSubmissionAttempts(model.Policy) {
		return w.fail(ctx, item, "SUBMIT_RETRIES_EXHAUSTED", "上游提交重试已耗尽", true)
	}
	tx, beginErr := w.DB.Begin(ctx)
	if beginErr != nil {
		return beginErr
	}
	defer tx.Rollback(ctx)
	command, updateErr := tx.Exec(ctx, `UPDATE generation_jobs SET status='dispatched',next_attempt_at=now()+$2::interval,
		error_code=$3,error_message=$4,retryable=true,updated_at=now() WHERE id=$1 AND status='submitting'`, item.JobID, pgInterval(delay), providerErr.Code, userFacingGenerationError(providerErr.Code))
	if updateErr != nil {
		return updateErr
	}
	if command.RowsAffected() != 1 {
		return river.JobSnooze(100 * time.Millisecond)
	}
	if _, updateErr = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.retry_scheduled',jsonb_build_object('status','dispatched','retry_after_ms',$4::bigint))`, item.OwnerID, item.BatchID, item.JobID, delay.Milliseconds()); updateErr != nil {
		return updateErr
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return commitErr
	}
	return river.JobSnooze(boundedSnooze(delay, item.GenerationDeadline))
}

func (w *GenerateWorker) pauseProvider(ctx context.Context, item generationRecord, model modelconfig.Model, providerErr *provider.Error) error {
	if w.Breaker != nil {
		w.Breaker.ForceOpen(item.ProviderID+":"+item.ModelID, time.Duration(model.Policy.BreakerCooldownSeconds)*time.Second)
	}
	if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='paused',last_error_code=$2,last_error_at=now(),updated_at=now() WHERE id=$1`, item.ProviderID, providerErr.Code); err != nil {
		if w.Log != nil {
			w.Log.Error("provider pause could not be persisted", "provider", item.ProviderID, "error", err)
		}
		return fmt.Errorf("persist provider pause: %w", err)
	}
	return nil
}

func safeRetryDelay(jobID uuid.UUID, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := attempt - 1
	if exponent > 5 {
		exponent = 5
	}
	ceiling := time.Second * time.Duration(1<<exponent)
	seed := int(jobID[0])<<8 | int(jobID[1])
	fraction := float64((seed+attempt*97)%1000) / 1000
	return time.Duration(float64(ceiling) * fraction)
}

func (w *GenerateWorker) persistSubmissionUncertain(jobID uuid.UUID, code, message string, upstreamMayBeActive bool) error {
	persistCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	item, err := w.load(persistCtx, jobID)
	if err != nil {
		return err
	}
	return w.markUncertain(persistCtx, item, code, message, upstreamMayBeActive)
}

// RunSubmissionRecovery closes the hard-kill gap where the process cannot run
// its normal deferred persistence. A started submit attempt is written before
// network I/O, so an overdue unfinished row is durable proof that the request
// may have reached the provider and must never be submitted again.
func (w *GenerateWorker) RunSubmissionRecovery(ctx context.Context) {
	w.recoverUnfinishedSubmissions(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.recoverUnfinishedSubmissions(ctx)
		}
	}
}

func (w *GenerateWorker) recoverUnfinishedSubmissions(ctx context.Context) {
	rows, err := w.DB.Query(ctx, `SELECT j.id,a.created_at
		FROM generation_jobs j
		JOIN LATERAL (
			SELECT created_at FROM provider_attempts
			WHERE job_id=j.id AND operation='submit' AND outcome='started' AND finished_at IS NULL
			ORDER BY id DESC LIMIT 1
		) a ON true
		WHERE j.status='submitting' AND j.provider_job_id IS NULL
		ORDER BY a.created_at LIMIT 64`)
	if err != nil {
		if w.Log != nil && ctx.Err() == nil {
			w.Log.Warn("unfinished submission scan failed", "error", err)
		}
		return
	}
	type candidate struct {
		jobID     uuid.UUID
		startedAt time.Time
	}
	candidates := make([]candidate, 0, 64)
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.jobID, &item.startedAt); err != nil {
			break
		}
		candidates = append(candidates, item)
	}
	if rowsErr := rows.Err(); err == nil {
		err = rowsErr
	}
	rows.Close()
	if err != nil {
		if w.Log != nil && ctx.Err() == nil {
			w.Log.Warn("unfinished submission scan could not be read", "error", err)
		}
		return
	}
	for _, candidate := range candidates {
		item, loadErr := w.load(ctx, candidate.jobID)
		if loadErr != nil || item.Status != "submitting" || item.ProviderJobID != nil {
			continue
		}
		deadline := candidate.startedAt.Add(time.Duration(item.ModelSnapshot.Policy.SubmitTimeoutSeconds)*time.Second + 30*time.Second)
		if time.Now().Before(deadline) {
			continue
		}
		if persistErr := w.persistSubmissionUncertain(item.JobID, "SUBMISSION_UNCERTAIN", "Provider submission did not finish before the durable recovery deadline; automatic resubmission is disabled", true); persistErr != nil && w.Log != nil {
			w.Log.Error("unfinished submission could not be recovered", "generation_job_id", item.JobID, "error", persistErr)
		}
	}
}

func (w *GenerateWorker) markUncertain(ctx context.Context, item generationRecord, code, message string, upstreamMayBeActive bool) error {
	publicMessage := "任务提交结果不确定，请等待核查或移除记录"
	deadline := uncertainStateDeadline(time.Now(), item, upstreamMayBeActive)
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var providerJobID *string
	if err = tx.QueryRow(ctx, `SELECT status,provider_job_id FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&status, &providerJobID); err != nil {
		return err
	}
	if isBusinessTerminal(status) {
		return tx.Commit(ctx)
	}
	// Once an authenticated provider job identifier is durable, the task is no
	// longer an uncertain submission and must stay on the poll/ingest path.
	if providerJobID != nil {
		return tx.Commit(ctx)
	}
	if status == "cancelling" {
		return w.markCancelledInTx(ctx, tx, item, false, deadline)
	}
	if status != "submitting" && status != "ingesting" {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE provider_attempts SET outcome='uncertain',error_code=$2,error_message=$3,finished_at=now()
		WHERE id=(SELECT id FROM provider_attempts WHERE job_id=$1 AND operation='submit' AND outcome='started' AND finished_at IS NULL ORDER BY id DESC LIMIT 1)`, item.JobID, code, boundedAttemptMessage(message)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='submission_uncertain',submission_uncertain=true,error_code=$2,error_message=$3,
		dispatch_state='finished',upstream_active_until=$4,completed_at=now(),updated_at=now() WHERE id=$1`, item.JobID, code, publicMessage, deadline); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.submission_uncertain',jsonb_build_object('status','submission_uncertain','error_code',$4::text))`, item.OwnerID, item.BatchID, item.JobID, code); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *GenerateWorker) fail(ctx context.Context, item generationRecord, code, message string, retryable bool) error {
	return w.failWithUpstreamLease(ctx, item, code, message, retryable, nil)
}

func (w *GenerateWorker) failWithUpstreamLease(ctx context.Context, item generationRecord, code, _ string, retryable bool, upstreamActiveUntil *time.Time) error {
	message := userFacingGenerationError(code)
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&status); err != nil {
		return err
	}
	if isBusinessTerminal(status) {
		return tx.Commit(ctx)
	}
	if status == "cancelling" {
		return w.markCancelledInTx(ctx, tx, item, false, upstreamActiveUntil)
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='failed',dispatch_state='finished',error_code=$2,error_message=$3,
		retryable=$4,upstream_active_until=$5,completed_at=now(),updated_at=now() WHERE id=$1`, item.JobID, code, message, retryable, upstreamActiveUntil); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.failed',jsonb_build_object('status','failed','error_code',$4::text,'message',$5::text,'retryable',$6::boolean,'upstream_may_still_be_active',$7::boolean))`, item.OwnerID, item.BatchID, item.JobID, code, message, retryable, upstreamActiveUntil != nil); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func userFacingGenerationError(code string) string {
	switch code {
	case "CONTENT_POLICY_REJECTED":
		return "图片可能触发安全策略，请调整描述"
	case "UNSUPPORTED_PARAMETER", "PROVIDER_HTTP_400", "PROVIDER_HTTP_413", "PROVIDER_HTTP_422":
		return "当前参数无法生成，请调整后重试"
	case "PROVIDER_IMAGE_INVALID", "PROVIDER_RESPONSE_INVALID", "PROVIDER_EMPTY_RESULT", "PROVIDER_OUTPUT_COUNT_INVALID":
		return "生成结果无法处理，请调整参数后重试"
	case "PROVIDER_HTTP_429", "LEGNEXT_TASK_FAILED", "SUBMIT_RETRIES_EXHAUSTED":
		return "生成服务繁忙，请稍后重试"
	case "REFERENCE_READ_FAILED":
		return "参考图无法读取，请重新添加后重试"
	default:
		return "生成失败，请稍后重试"
	}
}

func (w *GenerateWorker) markCancelled(ctx context.Context, item generationRecord, lateResult bool) error {
	return w.markCancelledWithUpstreamLease(ctx, item, lateResult, nil, nil)
}

func (w *GenerateWorker) markCancelledWithUpstreamLease(ctx context.Context, item generationRecord, lateResult bool, upstreamActiveUntil *time.Time, finalCancelMode *string) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&status); err != nil {
		return err
	}
	if status == "succeeded" || status == "failed" || status == "submission_uncertain" {
		return tx.Commit(ctx)
	}
	if status != "cancelled" {
		var persistedCancelMode *string
		if err = tx.QueryRow(ctx, `UPDATE generation_jobs SET status='cancelled',dispatch_state='finished',
			upstream_active_until=$2,cancel_mode=COALESCE($3,cancel_mode),completed_at=COALESCE(completed_at,now()),updated_at=now()
			WHERE id=$1 RETURNING cancel_mode`, item.JobID, upstreamActiveUntil, finalCancelMode).Scan(&persistedCancelMode); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
			VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',$4::boolean,
			'cancel_mode',$5::text,'cost_may_have_been_incurred',COALESCE($5::text='discard_result_only',false)))`, item.OwnerID, item.BatchID, item.JobID, lateResult, persistedCancelMode); err != nil {
			return err
		}
	} else if upstreamActiveUntil != nil {
		// A duplicate control delivery must not shorten an existing conservative
		// lease. The hard deadline still bounds the row automatically in queries.
		if _, err = tx.Exec(ctx, `UPDATE generation_jobs
			SET upstream_active_until=GREATEST(upstream_active_until,$2),cancel_mode=COALESCE($3,cancel_mode),updated_at=now()
			WHERE id=$1`, item.JobID, upstreamActiveUntil, finalCancelMode); err != nil {
			return err
		}
	} else if finalCancelMode != nil {
		if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET cancel_mode=$2,updated_at=now() WHERE id=$1`, item.JobID, finalCancelMode); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *GenerateWorker) markCancelledInTx(ctx context.Context, tx pgx.Tx, item generationRecord, lateResult bool, upstreamActiveUntil *time.Time) error {
	var persistedCancelMode *string
	if err := tx.QueryRow(ctx, `UPDATE generation_jobs SET status='cancelled',dispatch_state='finished',
		upstream_active_until=$2,completed_at=COALESCE(completed_at,now()),updated_at=now()
		WHERE id=$1 RETURNING cancel_mode`, item.JobID, upstreamActiveUntil).Scan(&persistedCancelMode); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
		VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',$4::boolean,
		'cancel_mode',$5::text,'cost_may_have_been_incurred',COALESCE($5::text='discard_result_only',false)))`, item.OwnerID, item.BatchID, item.JobID, lateResult, persistedCancelMode); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err := reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *GenerateWorker) cancelRemote(ctx context.Context, item generationRecord, adapter provider.Adapter, submission provider.Submission) (provider.CancelResult, error) {
	if submission.ProviderJobID == "" {
		return provider.CancelResult{Accepted: true, Mode: "local"}, nil
	}
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	attemptID, attemptErr := w.beginAttempt(cancelCtx, item, "cancel")
	if attemptErr != nil {
		return provider.CancelResult{}, attemptErr
	}
	started := time.Now()
	result, err := adapter.Cancel(cancelCtx, submission)
	w.finishAttempt(attemptID, item, "cancel", time.Since(started), err, nil, result.Telemetry)
	return result, err
}

func (w *GenerateWorker) completeCancellation(ctx context.Context, item generationRecord, adapter provider.Adapter, submission provider.Submission, lateResult bool) (bool, error) {
	result, cancelErr := w.cancelRemote(ctx, item, adapter, submission)
	deadline := cancelledUpstreamDeadline(time.Now(), item, submission, result, cancelErr)
	finalMode := resolvedCancelMode(item, submission, result, cancelErr)
	if err := w.markCancelledWithUpstreamLease(ctx, item, lateResult, deadline, &finalMode); err != nil {
		return false, err
	}
	return deadline != nil, nil
}

func cancelledUpstreamDeadline(now time.Time, item generationRecord, submission provider.Submission, result provider.CancelResult, cancelErr error) *time.Time {
	if submission.ProviderJobID == "" {
		// Cancelling a job whose Submit call was interrupted is not a local-only
		// cancellation. The API records discard_result_only for that state because
		// the provider may have accepted the request before the worker crashed.
		if item.CancelMode == "discard_result_only" {
			return upstreamHardDeadline(now, item)
		}
		return nil
	}
	// OpenRouter image generation is synchronous. A persisted response ID means
	// the paid call has already returned, so there is no remote execution left
	// to occupy even when its result is discarded locally.
	if item.ProviderID == "openrouter" || item.ProviderID == "mock" {
		return nil
	}
	if cancelErr == nil && result.Accepted {
		return nil
	}
	return upstreamHardDeadline(now, item)
}

func resolvedCancelMode(item generationRecord, submission provider.Submission, result provider.CancelResult, cancelErr error) string {
	if submission.ProviderJobID == "" {
		if item.CancelMode == "discard_result_only" {
			return "discard_result_only"
		}
		return "local"
	}
	if cancelErr == nil && result.Accepted {
		return "requested_upstream"
	}
	return "discard_result_only"
}

func uncertainSubmissionDeadline(now time.Time, item generationRecord, submissionErr error) *time.Time {
	var providerErr *provider.Error
	if !errors.As(submissionErr, &providerErr) || !providerErr.SubmissionUncertain {
		return nil
	}
	return upstreamHardDeadline(now, item)
}

func uncertainStateDeadline(now time.Time, item generationRecord, upstreamMayBeActive bool) *time.Time {
	if !upstreamMayBeActive {
		return nil
	}
	return upstreamHardDeadline(now, item)
}

func upstreamHardDeadline(now time.Time, item generationRecord) *time.Time {
	if item.GenerationDeadline != nil {
		deadline := item.GenerationDeadline.UTC().Add(upstreamTrackingGrace(item))
		if !now.Before(deadline) {
			return nil
		}
		return &deadline
	}
	deadline := now.UTC().Add(upstreamTrackingGrace(item))
	return &deadline
}

func upstreamTrackingGrace(item generationRecord) time.Duration {
	seconds := item.ModelSnapshot.Policy.GenerationTimeoutSeconds
	if seconds < 300 {
		seconds = 900
	}
	if seconds > 3600 {
		seconds = 3600
	}
	return time.Duration(seconds) * time.Second
}

func timedOutUpstreamDeadline(now time.Time, item generationRecord, submission provider.Submission, result provider.CancelResult, cancelErr error) *time.Time {
	if submission.ProviderJobID == "" || item.ProviderID == "openrouter" || item.ProviderID == "mock" {
		return nil
	}
	if cancelErr == nil && result.Accepted {
		return nil
	}
	return upstreamHardDeadline(now, item)
}

func upstreamLeaseActive(now time.Time, until *time.Time) bool {
	return until != nil && now.Before(*until)
}

func providerResultTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func providerOutputCountExceeded(actual, expected int) bool {
	return expected > 0 && actual > expected
}

func (w *GenerateWorker) observeCancelledUpstream(ctx context.Context, item generationRecord, adapter provider.Adapter) error {
	return w.observeTerminalUpstream(ctx, item, adapter)
}

func (w *GenerateWorker) observeTerminalUpstream(ctx context.Context, item generationRecord, adapter provider.Adapter) error {
	if !upstreamLeaseActive(time.Now(), item.UpstreamActiveUntil) {
		if err := w.clearUpstreamLease(ctx, item.JobID); err != nil {
			return err
		}
		return river.JobCancel(nil)
	}
	// An ambiguous submission may be active even though no provider ID was
	// returned. It cannot be polled safely, so only the persisted hard deadline
	// can release its conservative occupancy.
	if item.ProviderJobID == nil {
		return river.JobSnooze(boundedSnooze(30*time.Second, item.UpstreamActiveUntil))
	}
	release, err := w.acquireProvider(ctx, item.ProviderID)
	if err != nil {
		return err
	}
	attemptID, attemptErr := w.beginAttempt(ctx, item, "cancelled_poll")
	if attemptErr != nil {
		release()
		return attemptErr
	}
	pollCtx, cancelPoll := boundedContext(ctx, 45*time.Second, item.UpstreamActiveUntil)
	started := time.Now()
	result, pollErr := adapter.Poll(pollCtx, submissionFromRecord(item))
	cancelPoll()
	release()
	observedErr := observedResultError(result, pollErr)
	if pollErr != nil || terminalProviderResult(result.Status) {
		w.recordPassiveBreaker(ctx, item, item.ModelSnapshot, item.ProviderID+":"+item.ModelID, observedErr)
	}
	w.finishAttempt(attemptID, item, "cancelled_poll", time.Since(started), observedErr, result.Usage, result.Telemetry)
	if pollErr == nil && providerResultTerminal(result.Status) {
		if err := w.clearUpstreamLease(ctx, item.JobID); err != nil {
			return err
		}
		return river.JobCancel(nil)
	}
	return river.JobSnooze(boundedSnooze(3*time.Second, item.UpstreamActiveUntil))
}

func (w *GenerateWorker) clearUpstreamLease(ctx context.Context, jobID uuid.UUID) error {
	_, err := w.DB.Exec(ctx, `UPDATE generation_jobs SET upstream_active_until=NULL,updated_at=now()
		WHERE id=$1 AND upstream_active_until IS NOT NULL`, jobID)
	return err
}

func (w *GenerateWorker) ingestStaged(ctx context.Context, item generationRecord) error {
	staged, err := w.loadStagedOutputs(ctx, item.JobID)
	if err != nil {
		return err
	}
	if len(staged) == 0 {
		return w.fail(ctx, item, "STAGED_RESULT_MISSING", "已接收的同步生成结果缺少可恢复暂存数据", false)
	}
	for index := range staged {
		output := &staged[index]
		path, resolveErr := w.Blobs.Resolve(output.StorageKey)
		if resolveErr != nil {
			return resolveErr
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return statErr
		}
		if info.Size() != output.ByteSize {
			return fmt.Errorf("staged output size mismatch for index %d", output.OutputIndex)
		}
		if output.BlurDataURL == "" || !w.presentationVariantsReady(output.StorageKey) {
			blurDataURL, presentationErr := w.ensurePresentationVariants(ctx, output.StorageKey)
			if presentationErr != nil {
				return presentationErr
			}
			output.BlurDataURL = blurDataURL
			if _, updateErr := w.DB.Exec(ctx, `UPDATE generation_staged_outputs SET blur_data_url=$3 WHERE job_id=$1 AND output_index=$2`, item.JobID, output.OutputIndex, blurDataURL); updateErr != nil {
				return updateErr
			}
		}
	}
	select {
	case w.IngestSem <- struct{}{}:
		defer func() { <-w.IngestSem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1 FOR UPDATE`, item.JobID).Scan(&status); err != nil {
		return err
	}
	if status == "cancelled" || status == "cancelling" {
		if _, err = tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
			return err
		}
		if status == "cancelling" {
			if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='cancelled',dispatch_state='finished',completed_at=now(),updated_at=now() WHERE id=$1`, item.JobID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
				VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',true))`, item.OwnerID, item.BatchID, item.JobID); err != nil {
				return err
			}
		}
		if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if status == "succeeded" {
		if _, err = tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if status != "ingesting" {
		return fmt.Errorf("cannot ingest staged generation job in status %s", status)
	}

	newKeys := make([]string, 0, len(staged))
	for _, output := range staged {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM generation_outputs WHERE job_id=$1 AND output_index=$2)`, item.JobID, output.OutputIndex).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		var assetID uuid.UUID
		if err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size,blur_data_url)
			VALUES($1,'generation',$2,$3,$4,$5,$6,$7,$8) RETURNING id`, item.OwnerID, output.StorageKey, output.SHA256, output.MediaType, output.Width, output.Height, output.ByteSize, output.BlurDataURL).Scan(&assetID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO generation_outputs(job_id,asset_id,output_index) VALUES($1,$2,$3)`, item.JobID, assetID, output.OutputIndex); err != nil {
			return err
		}
		newKeys = append(newKeys, output.StorageKey)
	}

	outputRows, err := tx.Query(ctx, `SELECT o.asset_id,o.output_index,a.width,a.height,a.media_type,a.byte_size,a.sha256,COALESCE(a.blur_data_url,''),a.created_at
		FROM generation_outputs o JOIN assets a ON a.id=o.asset_id WHERE o.job_id=$1 ORDER BY o.output_index`, item.JobID)
	if err != nil {
		return err
	}
	eventOutputs := make([]map[string]any, 0, item.ExpectedOutputs)
	eventAssets := make([]map[string]any, 0, item.ExpectedOutputs)
	for outputRows.Next() {
		var assetID uuid.UUID
		var outputIndex, width, height int
		var mediaType, digest, blurDataURL string
		var byteSize int64
		var createdAt time.Time
		if err = outputRows.Scan(&assetID, &outputIndex, &width, &height, &mediaType, &byteSize, &digest, &blurDataURL, &createdAt); err != nil {
			outputRows.Close()
			return err
		}
		eventOutputs = append(eventOutputs, map[string]any{
			"asset_id": assetID, "output_index": outputIndex, "width": width, "height": height, "media_type": mediaType,
			"thumb_320_url":  "/api/v1/assets/" + assetID.String() + "/content?variant=320",
			"thumb_640_url":  "/api/v1/assets/" + assetID.String() + "/content?variant=640",
			"thumb_1280_url": "/api/v1/assets/" + assetID.String() + "/content?variant=1280",
		})
		baseURL := "/api/v1/assets/" + assetID.String() + "/content"
		eventAssets = append(eventAssets, map[string]any{
			"id": assetID, "kind": "generation", "job_id": item.JobID, "batch_id": item.BatchID,
			"output_index": outputIndex, "width": width, "height": height, "media_type": mediaType,
			"byte_size": byteSize, "sha256": digest, "blur_data_url": blurDataURL,
			"url": baseURL, "thumb_320_url": baseURL + "?variant=320", "thumb_640_url": baseURL + "?variant=640",
			"thumb_1280_url": baseURL + "?variant=1280", "created_at": createdAt,
		})
	}
	if err = outputRows.Err(); err != nil {
		outputRows.Close()
		return err
	}
	outputRows.Close()
	encodedOutputs, err := json.Marshal(eventOutputs)
	if err != nil {
		return err
	}
	encodedAssets, err := json.Marshal(eventAssets)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='succeeded',dispatch_state='finished',completed_at=now(),error_code=NULL,error_message=NULL,updated_at=now() WHERE id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
		VALUES($1,$2,$3,'job.succeeded',jsonb_build_object('status','succeeded','output_count',$4::integer,'outputs',$5::jsonb,'assets',$6::jsonb))`, item.OwnerID, item.BatchID, item.JobID, len(eventOutputs), string(encodedOutputs), string(encodedAssets)); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, key := range newKeys {
		w.queueOptionalThumbnail(key)
	}
	return nil
}

type workerBatchCounts struct {
	total      int
	succeeded  int
	failed     int
	cancelled  int
	cancelling int
	running    int
}

func desiredWorkerBatchStatus(counts workerBatchCounts) string {
	terminal := counts.succeeded + counts.failed + counts.cancelled
	if counts.total > 0 && terminal == counts.total {
		switch {
		case counts.succeeded == counts.total:
			return "succeeded"
		case counts.succeeded > 0:
			return "partial"
		case counts.cancelled == counts.total:
			return "cancelled"
		default:
			return "failed"
		}
	}
	if counts.cancelling > 0 {
		return "cancelling"
	}
	if counts.running > 0 {
		return "running"
	}
	return "queued"
}

func workerBatchStatusWithOutputCount(status string, completed, expected int) string {
	if status == "succeeded" && expected > 0 && completed != expected {
		return "partial"
	}
	return status
}

func reconcileWorkerBatch(ctx context.Context, tx pgx.Tx, batchID uuid.UUID) (string, error) {
	var ownerID uuid.UUID
	var currentStatus string
	var currentCompleted, expectedOutputs int
	if err := tx.QueryRow(ctx, `SELECT owner_user_id,status,completed_outputs,expected_outputs FROM generation_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&ownerID, &currentStatus, &currentCompleted, &expectedOutputs); err != nil {
		return "", err
	}
	var counts workerBatchCounts
	if err := tx.QueryRow(ctx, `SELECT count(*)::int,
		count(*) FILTER (WHERE status='succeeded')::int,
		count(*) FILTER (WHERE status IN ('failed','submission_uncertain'))::int,
		count(*) FILTER (WHERE status='cancelled')::int,
		count(*) FILTER (WHERE status='cancelling')::int,
		count(*) FILTER (WHERE status IN ('dispatched','submitting','provider_pending','ingesting'))::int
		FROM generation_jobs WHERE batch_id=$1`, batchID).Scan(&counts.total, &counts.succeeded, &counts.failed, &counts.cancelled, &counts.cancelling, &counts.running); err != nil {
		return "", err
	}
	var completed int
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM generation_outputs o JOIN generation_jobs j ON j.id=o.job_id WHERE j.batch_id=$1`, batchID).Scan(&completed); err != nil {
		return "", err
	}
	status := workerBatchStatusWithOutputCount(desiredWorkerBatchStatus(counts), completed, expectedOutputs)
	if status == currentStatus && completed == currentCompleted {
		return status, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE generation_batches SET status=$2,completed_outputs=$3,updated_at=now() WHERE id=$1`, batchID, status, completed); err != nil {
		return "", err
	}
	eventType := "batch.updated"
	if status == "failed" {
		eventType = "batch.failed"
	} else if status == "succeeded" || status == "partial" || status == "cancelled" {
		eventType = "batch.completed"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,event_type,payload) VALUES($1,$2,$3,jsonb_build_object('status',$4::text,'completed_outputs',$5::integer))`, ownerID, batchID, eventType, status, completed); err != nil {
		return "", err
	}
	return status, nil
}

func (w *GenerateWorker) downloadToFile(ctx context.Context, item generationRecord, rawURL, target string) (err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return &provider.Error{Code: "OUTPUT_URL_REJECTED", Message: "invalid output URL"}
	}
	if !generationOutputURLAllowed(parsed, item.ModelSnapshot.Policy.AllowedOutputHosts) {
		return &provider.Error{Code: "OUTPUT_HOST_REJECTED", Message: "output host is not allowlisted"}
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		retry, retryAfter, lastErr := w.downloadToFileOnce(ctx, item, rawURL, target)
		if lastErr == nil || !retry || attempt == 2 {
			return lastErr
		}
		if retryAfter <= 0 {
			// Keep result recovery responsive while spreading concurrent four-image
			// retries. This path never resubmits the paid generation request.
			ceiling := 250 * time.Millisecond * time.Duration(1<<attempt)
			retryAfter = time.Duration(rand.Int64N(int64(ceiling) + 1))
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

func (w *GenerateWorker) downloadToFileOnce(ctx context.Context, item generationRecord, rawURL, target string) (retry bool, retryAfter time.Duration, err error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	res, err := w.HTTPClient.Do(req)
	if err != nil {
		// net/http wraps transport failures in url.Error, whose string contains
		// the complete provider URL. CDN query parameters can be bearer tokens,
		// so never let that error cross into River or structured logs.
		return true, 0, &provider.Error{Code: "OUTPUT_DOWNLOAD_FAILED", Message: "provider result download failed", Retryable: true}
	}
	defer res.Body.Close()
	if res.Request == nil || res.Request.URL == nil || !generationOutputURLAllowed(res.Request.URL, item.ModelSnapshot.Policy.AllowedOutputHosts) {
		return false, 0, &provider.Error{Code: "OUTPUT_HOST_REJECTED", Message: "redirected output host is not allowlisted"}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		providerErr := &provider.Error{Code: fmt.Sprintf("OUTPUT_HTTP_%d", res.StatusCode), Message: fmt.Sprintf("download result: HTTP %d", res.StatusCode), Telemetry: provider.Telemetry{HTTPStatus: res.StatusCode}}
		retry = res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500
		if retry {
			retryAfter = parseDownloadRetryAfter(res.Header.Get("Retry-After"), time.Now())
			providerErr.Retryable = true
		}
		return retry, retryAfter, providerErr
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return false, 0, err
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	const maximum = int64(50 * 1024 * 1024)
	written, err := io.Copy(f, io.LimitReader(res.Body, maximum+1))
	if err != nil {
		return true, 0, &provider.Error{Code: "OUTPUT_DOWNLOAD_FAILED", Message: "provider result download failed", Retryable: true}
	}
	if written > maximum {
		return false, 0, errors.New("result image exceeds limit")
	}
	if err = f.Sync(); err != nil {
		return false, 0, err
	}
	return false, 0, nil
}

func parseDownloadRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

func generationOutputURLAllowed(parsed *url.URL, patterns []string) bool {
	if parsed == nil || parsed.Scheme != "https" {
		return false
	}
	for _, pattern := range patterns {
		if outputHostAllowed(parsed.Hostname(), pattern) {
			return true
		}
	}
	return false
}

func outputHostAllowed(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	pattern = strings.ToLower(pattern)
	if host == pattern {
		return true
	}
	if pattern != "delivery.*.bfl.ai" || !strings.HasPrefix(host, "delivery.") || !strings.HasSuffix(host, ".bfl.ai") {
		return false
	}
	region := strings.TrimSuffix(strings.TrimPrefix(host, "delivery."), ".bfl.ai")
	return region != "" && !strings.Contains(region, ".")
}

func (w *GenerateWorker) ensurePresentationVariants(ctx context.Context, key string) (string, error) {
	select {
	case w.ThumbSem <- struct{}{}:
		defer func() { <-w.ThumbSem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	original, err := w.Blobs.Resolve(key)
	if err != nil {
		return "", err
	}
	for _, size := range []string{"320", "640"} {
		if err := createThumbnailVariant(ctx, original, size, 82); err != nil {
			return "", fmt.Errorf("create required thumbnail %s: %w", size, err)
		}
	}
	for _, placeholder := range []struct {
		size    string
		quality int
	}{{"24", 35}, {"16", 25}} {
		blurPath := thumbnailTempPath(filepath.Dir(original), "blur")
		if err := runVIPSThumbnail(ctx, original, blurPath, placeholder.size, placeholder.quality); err != nil {
			_ = os.Remove(blurPath)
			return "", fmt.Errorf("create blur placeholder: %w", err)
		}
		blur, readErr := os.ReadFile(blurPath)
		_ = os.Remove(blurPath)
		if readErr != nil {
			return "", readErr
		}
		dataURL := "data:image/webp;base64," + base64.StdEncoding.EncodeToString(blur)
		if len(dataURL) <= 4096 {
			return dataURL, nil
		}
	}
	return "", errors.New("blur placeholder exceeds 4 KiB")
}

func (w *GenerateWorker) presentationVariantsReady(key string) bool {
	original, err := w.Blobs.Resolve(key)
	if err != nil {
		return false
	}
	for _, size := range []string{"320", "640"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(original), "thumb-"+size+".webp")); err != nil {
			return false
		}
	}
	return true
}

func (w *GenerateWorker) makeOptionalThumbnail(ctx context.Context, key, size string) {
	select {
	case w.ThumbSem <- struct{}{}:
		defer func() { <-w.ThumbSem }()
	case <-ctx.Done():
		return
	}
	original, err := w.Blobs.Resolve(key)
	if err == nil {
		err = createThumbnailVariant(ctx, original, size, 82)
	}
	if err != nil && w.Log != nil {
		w.Log.Warn("optional thumbnail failed", "error", err, "asset", key, "variant", size)
	}
}

func (w *GenerateWorker) queueOptionalThumbnail(key string) {
	if w.OptionalThumbQueue == nil {
		return
	}
	select {
	case w.OptionalThumbQueue <- key:
	default:
		if w.Log != nil {
			w.Log.Warn("optional thumbnail queue full", "asset", key)
		}
	}
}

func (w *GenerateWorker) RunOptionalThumbnails(ctx context.Context, concurrency int) {
	if w.OptionalThumbQueue == nil || concurrency < 1 {
		return
	}
	done := make(chan struct{}, concurrency)
	for range concurrency {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case <-ctx.Done():
					return
				case key := <-w.OptionalThumbQueue:
					w.makeOptionalThumbnail(ctx, key, "1280")
				}
			}
		}()
	}
	for range concurrency {
		<-done
	}
}

func (w *GenerateWorker) makeThumbnails(ctx context.Context, key string) {
	if _, err := w.ensurePresentationVariants(ctx, key); err != nil {
		if w.Log != nil {
			w.Log.Warn("required thumbnails failed", "error", err, "asset", key)
		}
		return
	}
	w.makeOptionalThumbnail(ctx, key, "1280")
}

func createThumbnailVariant(ctx context.Context, original, size string, quality int) error {
	final := filepath.Join(filepath.Dir(original), "thumb-"+size+".webp")
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	temp := thumbnailTempPath(filepath.Dir(original), size)
	defer os.Remove(temp)
	if err := runVIPSThumbnail(ctx, original, temp, size, quality); err != nil {
		return err
	}
	return os.Rename(temp, final)
}

func runVIPSThumbnail(ctx context.Context, original, output, size string, quality int) error {
	thumbCtx, cancelThumb := context.WithTimeout(ctx, 60*time.Second)
	defer cancelThumb()
	command := exec.CommandContext(thumbCtx, "vipsthumbnail", original, "--size", size+"x", "--output", fmt.Sprintf("%s[Q=%d,strip]", output, quality))
	command.Env = append(os.Environ(), "VIPS_CONCURRENCY=2", "VIPS_DISC_THRESHOLD=268435456", "MALLOC_ARENA_MAX=2")
	if outputBytes, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("vipsthumbnail: %w: %s", err, strings.TrimSpace(string(outputBytes)))
	}
	return nil
}

func thumbnailTempPath(directory, size string) string {
	// libvips selects the encoder from the final suffix. Keep .webp last while
	// retaining a unique, recognizable partial name for atomic publication and
	// crash cleanup.
	return filepath.Join(directory, fmt.Sprintf(".thumb-%s-%s.part.webp", size, uuid.NewString()))
}

func (w *GenerateWorker) beginAttempt(ctx context.Context, item generationRecord, operation string) (int64, error) {
	var attemptID int64
	err := w.DB.QueryRow(ctx, `INSERT INTO provider_attempts(job_id,provider_id,operation,attempt_no,outcome)
		VALUES($1,$2,$3,(SELECT count(*)+1 FROM provider_attempts WHERE job_id=$1 AND operation=$3),'started') RETURNING id`, item.JobID, item.ProviderID, operation).Scan(&attemptID)
	return attemptID, err
}

func (w *GenerateWorker) finishAttempt(attemptID int64, item generationRecord, operation string, duration time.Duration, attemptErr error, usage map[string]any, telemetry provider.Telemetry) {
	outcome, code, message := "succeeded", "", ""
	if attemptErr != nil {
		outcome, message = "failed", boundedAttemptMessage(attemptErr.Error())
		var providerErr *provider.Error
		if errors.As(attemptErr, &providerErr) {
			code = providerErr.Code
			if telemetry.ProviderRequestID == "" {
				telemetry.ProviderRequestID = providerErr.Telemetry.ProviderRequestID
			}
			if telemetry.HTTPStatus == 0 {
				telemetry.HTTPStatus = providerErr.Telemetry.HTTPStatus
			}
		}
	}
	telemetry = telemetry.Normalized()
	usage = sanitizeAttemptUsage(usage)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWrite()
	command, err := w.DB.Exec(writeCtx, `UPDATE provider_attempts SET provider_request_id=NULLIF($2,''),http_status=NULLIF($3,0),
		duration_ms=$4,outcome=$5,error_code=NULLIF($6,''),error_message=NULLIF($7,''),usage=COALESCE($8,'{}'::jsonb),finished_at=now()
		WHERE id=$1 AND job_id=$9 AND operation=$10 AND outcome='started' AND finished_at IS NULL`, attemptID, telemetry.ProviderRequestID, telemetry.HTTPStatus, duration.Milliseconds(), outcome, code, message, usage, item.JobID, operation)
	if err == nil && command.RowsAffected() != 1 {
		err = fmt.Errorf("attempt lifecycle update affected %d rows", command.RowsAffected())
	}
	if err != nil && w.Log != nil {
		w.Log.Warn("provider attempt telemetry could not be persisted",
			"generation_job_id", item.JobID,
			"provider", item.ProviderID,
			"operation", operation,
			"provider_request_id", telemetry.ProviderRequestID,
			"http_status", telemetry.HTTPStatus,
			"error", err,
		)
	}
}

func boundedAttemptMessage(message string) string {
	const maxBytes = 1024
	if len(message) <= maxBytes {
		return message
	}
	end := maxBytes - len("...")
	for end > 0 && !utf8.ValidString(message[:end]) {
		end--
	}
	return message[:end] + "..."
}

var allowedAttemptUsageKeys = map[string]struct{}{
	"cached_tokens":           {},
	"completion_tokens":       {},
	"cost":                    {},
	"credits":                 {},
	"duration_ms":             {},
	"image_count":             {},
	"input_image_tokens":      {},
	"input_tokens":            {},
	"output_image_tokens":     {},
	"output_tokens":           {},
	"points":                  {},
	"prompt_tokens":           {},
	"reference_count":         {},
	"reasoning_tokens":        {},
	"total_cost":              {},
	"total_tokens":            {},
	"upstream_inference_cost": {},
}

var allowedAttemptUsageGroups = map[string]struct{}{
	"completion_tokens_details": {},
	"cost_details":              {},
	"prompt_tokens_details":     {},
	"token_details":             {},
}

func sanitizeAttemptUsage(usage map[string]any) map[string]any {
	clean := make(map[string]any)
	for key, value := range usage {
		if _, ok := allowedAttemptUsageKeys[key]; ok {
			if scalar, valid := safeUsageScalar(value); valid {
				clean[key] = scalar
			}
			continue
		}
		if _, ok := allowedAttemptUsageGroups[key]; !ok {
			continue
		}
		nested, ok := value.(map[string]any)
		if !ok {
			continue
		}
		cleanNested := make(map[string]any)
		for nestedKey, nestedValue := range nested {
			if _, ok := allowedAttemptUsageKeys[nestedKey]; !ok {
				continue
			}
			if scalar, valid := safeUsageScalar(nestedValue); valid {
				cleanNested[nestedKey] = scalar
			}
		}
		if len(cleanNested) > 0 {
			clean[key] = cleanNested
		}
	}
	return clean
}

func safeUsageScalar(value any) (any, bool) {
	switch number := value.(type) {
	case float64:
		return number, !math.IsNaN(number) && !math.IsInf(number, 0)
	case float32:
		return number, !float32IsNonFinite(number)
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return number, true
	default:
		return nil, false
	}
}

func float32IsNonFinite(value float32) bool {
	return math.IsNaN(float64(value)) || math.IsInf(float64(value), 0)
}

func (w *GenerateWorker) recordBreaker(ctx context.Context, item generationRecord, model modelconfig.Model, key string, attemptErr error) {
	if w.Breaker == nil {
		return
	}
	// Deterministic caller-side rejections do not say anything about provider
	// availability. Counting them would let one user trip the shared circuit
	// breaker with invalid or policy-rejected prompts. Abandon also releases a
	// half-open reservation without recording a false success.
	if breakerExemptError(attemptErr) {
		w.Breaker.Abandon(key)
		return
	}
	opened, until := w.Breaker.RecordPolicy(key, attemptErr == nil, model.Policy.BreakerMinRequests, model.Policy.BreakerFailureRatio, time.Duration(model.Policy.BreakerCooldownSeconds)*time.Second)
	if opened {
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='degraded',breaker_open_until=$2,last_error_code=$3,last_error_at=now(),updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID, until, providerErrorCode(attemptErr)); err != nil && w.Log != nil {
			w.Log.Warn("provider breaker state update failed", "provider", item.ProviderID, "error", err)
		}
	} else if attemptErr == nil {
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='healthy',breaker_open_until=NULL,last_probe_at=now(),last_error_code=NULL,updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID); err != nil && w.Log != nil {
			w.Log.Warn("provider breaker recovery update failed", "provider", item.ProviderID, "error", err)
		}
	}
}

func breakerExemptError(err error) bool {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		return false
	}
	switch providerErr.Code {
	case "CONTENT_POLICY_REJECTED", "UNSUPPORTED_PARAMETER", "REFERENCE_URL_INVALID", "PROVIDER_HTTP_400", "PROVIDER_HTTP_413", "PROVIDER_HTTP_422":
		return true
	}
	switch providerErr.Telemetry.HTTPStatus {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	case http.StatusForbidden:
		return !providerErr.PauseProvider
	default:
		return false
	}
}

func (w *GenerateWorker) recordPassiveBreaker(ctx context.Context, item generationRecord, model modelconfig.Model, key string, attemptErr error) {
	if w.Breaker == nil {
		return
	}
	// Polling can surface the same deterministic policy/caller rejections as
	// submission. Passive traffic has no half-open permit to release, but these
	// responses must still be excluded from shared availability statistics.
	if breakerExemptError(attemptErr) {
		return
	}
	opened, until := w.Breaker.RecordPassivePolicy(key, attemptErr == nil, model.Policy.BreakerMinRequests, model.Policy.BreakerFailureRatio, time.Duration(model.Policy.BreakerCooldownSeconds)*time.Second)
	if opened {
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='degraded',breaker_open_until=$2,last_error_code=$3,last_error_at=now(),updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID, until, providerErrorCode(attemptErr)); err != nil && w.Log != nil {
			w.Log.Warn("provider passive breaker state update failed", "provider", item.ProviderID, "error", err)
		}
	} else if attemptErr == nil {
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='healthy',breaker_open_until=NULL,last_probe_at=now(),last_error_code=NULL,updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID); err != nil && w.Log != nil {
			w.Log.Warn("provider passive breaker recovery update failed", "provider", item.ProviderID, "error", err)
		}
	}
}

func providerErrorCode(err error) string {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return providerErr.Code
	}
	return "PROVIDER_REQUEST_FAILED"
}

func (w *GenerateWorker) acquireProvider(ctx context.Context, providerID string) (func(), error) {
	sem := w.ProviderSem[providerID]
	if sem == nil {
		return func() {}, nil
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *GenerateWorker) currentStatus(ctx context.Context, jobID uuid.UUID) (string, error) {
	var status string
	err := w.DB.QueryRow(ctx, `SELECT status FROM generation_jobs WHERE id=$1`, jobID).Scan(&status)
	return status, err
}

func boundedContext(parent context.Context, timeout time.Duration, deadline *time.Time) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	wanted := time.Now().Add(timeout)
	if deadline != nil && deadline.Before(wanted) {
		return context.WithDeadline(parent, *deadline)
	}
	return context.WithTimeout(parent, timeout)
}

func boundedSnooze(delay time.Duration, deadline *time.Time) time.Duration {
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	if deadline == nil {
		return delay
	}
	remaining := time.Until(*deadline)
	if remaining <= 100*time.Millisecond {
		return 100 * time.Millisecond
	}
	if delay > remaining {
		return remaining
	}
	return delay
}

func isBusinessTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "submission_uncertain"
}

func normalizeExecutionGeneration(generation int) int {
	if generation == 0 {
		return 1
	}
	return generation
}

// River jobs created before execution generations were introduced have no
// execution_generation key. Keep generation one encoded the same way during a
// rolling deployment so the uniqueness check still finds those jobs.
func riverExecutionGeneration(generation int) int {
	if generation <= 1 {
		return 0
	}
	return generation
}

func validateImage(data []byte) (media, extension string, width, height int, err error) {
	contentType := http.DetectContentType(data)
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 64*1024*1024 {
		return "", "", 0, 0, errors.New("provider returned an invalid or oversized image")
	}
	switch format {
	case "jpeg":
		media, extension = "image/jpeg", "jpg"
	case "png":
		media, extension = "image/png", "png"
	case "webp":
		media, extension = "image/webp", "webp"
	default:
		return "", "", 0, 0, errors.New("provider returned an unsupported image format")
	}
	if contentType != media {
		return "", "", 0, 0, errors.New("provider image MIME mismatch")
	}
	return media, extension, config.Width, config.Height, nil
}

// validateProviderImage performs a complete pixel decode after the bounded
// header checks. DecodeConfig alone accepts some truncated files (for example,
// a PNG containing only a valid IHDR), which must never be committed as a
// successful generation asset. Uploads get an equivalent full-decode pass via
// the sandboxed libvips validator before promotion from quarantine.
func validateProviderImage(data []byte) (media, extension string, width, height int, err error) {
	media, extension, width, height, err = validateImage(data)
	if err != nil {
		return "", "", 0, 0, err
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", "", 0, 0, errors.New("provider returned a truncated or corrupt image")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != width || bounds.Dy() != height {
		return "", "", 0, 0, errors.New("provider image dimensions changed during full decoding")
	}
	expectedFormat := map[string]string{"image/jpeg": "jpeg", "image/png": "png", "image/webp": "webp"}[media]
	if format != expectedFormat {
		return "", "", 0, 0, errors.New("provider image format changed during full decoding")
	}
	return media, extension, width, height, nil
}

func validateProviderImageFile(path string) (media, extension string, width, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	defer f.Close()
	header := make([]byte, 512)
	n, readErr := io.ReadFull(f, header)
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", "", 0, 0, errors.New("provider returned an invalid image")
	}
	contentType := http.DetectContentType(header[:n])
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", "", 0, 0, err
	}
	config, format, err := image.DecodeConfig(f)
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 64*1024*1024 {
		return "", "", 0, 0, errors.New("provider returned an invalid or oversized image")
	}
	switch format {
	case "jpeg":
		media, extension = "image/jpeg", "jpg"
	case "png":
		media, extension = "image/png", "png"
	case "webp":
		media, extension = "image/webp", "webp"
	default:
		return "", "", 0, 0, errors.New("provider returned an unsupported image format")
	}
	if contentType != media {
		return "", "", 0, 0, errors.New("provider image MIME mismatch")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", "", 0, 0, err
	}
	decoded, decodedFormat, err := image.Decode(f)
	if err != nil {
		return "", "", 0, 0, errors.New("provider returned a truncated or corrupt image")
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != config.Width || bounds.Dy() != config.Height || decodedFormat != format {
		return "", "", 0, 0, errors.New("provider image changed during full decoding")
	}
	return media, extension, config.Width, config.Height, nil
}

func writeSynced(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func pgInterval(duration time.Duration) string {
	return fmt.Sprintf("%f seconds", duration.Seconds())
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func valueOrPointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
