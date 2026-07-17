package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	_ "golang.org/x/image/webp"

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
	DB          *pgxpool.Pool
	Config      config.Config
	Blobs       *blob.Local
	Adapters    map[string]provider.Adapter
	ProviderSem map[string]chan struct{}
	IngestSem   chan struct{}
	ThumbSem    chan struct{}
	HTTPClient  *http.Client
	Log         *slog.Logger
	Breaker     *Breaker
}

type generationRecord struct {
	JobID               uuid.UUID
	BatchID             uuid.UUID
	OwnerID             uuid.UUID
	Status              string
	CancelMode          string
	ExecutionGeneration int
	ProviderJobID       *string
	ExpectedOutputs     int
	Prompt              string
	AspectRatio         string
	Resolution          string
	ModelID             string
	ProviderID          string
	ModelSnapshot       modelconfig.Model
	AttemptCount        int
	GenerationDeadline  *time.Time
	UpstreamActiveUntil *time.Time
}

type submissionClaim struct {
	Claimed    bool
	RetryAfter time.Duration
	Reason     string
	Attempt    int
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
		if err == nil || job.Attempt < job.MaxAttempts || errors.Is(err, context.Canceled) {
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
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
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
		tracked, err := w.completeCancellation(ctx, record, adapter, provider.Submission{ProviderJobID: valueOrPointer(record.ProviderJobID)}, false)
		if err != nil {
			return err
		}
		if tracked {
			return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
		}
		return river.JobCancel(nil)
	case "submitting":
		if record.ProviderJobID == nil {
			return w.markUncertain(ctx, record, "SUBMISSION_INTERRUPTED", "Worker 在提交期间中断，已停止自动重试", true)
		}
	}

	if record.GenerationDeadline != nil && !time.Now().Before(*record.GenerationDeadline) {
		if record.ProviderJobID != nil {
			submission := provider.Submission{ProviderJobID: *record.ProviderJobID}
			// A task can complete just before its deadline while the Worker is
			// restarting. Perform one authenticated final read before declaring a
			// paid result lost; the ordinary deadline-bounded poll would already be
			// cancelled at this point.
			release, acquireErr := w.acquireProvider(ctx, record.ProviderID)
			if acquireErr != nil {
				return acquireErr
			}
			finalPollCtx, cancelFinalPoll := context.WithTimeout(ctx, 45*time.Second)
			started := time.Now()
			finalResult, finalPollErr := adapter.Poll(finalPollCtx, submission)
			cancelFinalPoll()
			release()
			breakerKey := record.ProviderID + ":" + record.ModelID
			w.recordPassiveBreaker(ctx, record, model, breakerKey, finalPollErr)
			w.recordAttempt(ctx, record, "deadline_poll", time.Since(started), finalPollErr, finalResult.Usage, finalResult.Telemetry)
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
					return w.ingest(ctx, record, finalResult)
				case "failed":
					return w.fail(ctx, record, valueOr(finalResult.ErrorCode, "PROVIDER_JOB_FAILED"), valueOr(finalResult.ErrorText, "上游生成失败"), false)
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
		w.recordAttempt(ctx, record, "submit", time.Since(started), submitErr, submitUsage, submission.Telemetry)
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
			staged, stageErr := w.stageSynchronousResult(ctx, record, submission)
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
				persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				defer cancelPersist()
				if persistErr := w.markUncertain(persistCtx, record, "RESULT_STAGING_FAILED", "同步生成已完成，但结果无法可靠暂存；系统不会自动重新提交", false); persistErr != nil {
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
	pollCtx, cancelPoll := boundedContext(ctx, 45*time.Second, record.GenerationDeadline)
	started := time.Now()
	result, pollErr := adapter.Poll(pollCtx, provider.Submission{ProviderJobID: *record.ProviderJobID})
	cancelPoll()
	release()
	w.recordPassiveBreaker(ctx, record, model, breakerKey, pollErr)
	w.recordAttempt(ctx, record, "poll", time.Since(started), pollErr, result.Usage, result.Telemetry)
	if pollErr != nil {
		if status, statusErr := w.currentStatus(ctx, record.JobID); statusErr == nil && (status == "cancelling" || status == "cancelled") {
			tracked, cancelErr := w.completeCancellation(ctx, record, adapter, provider.Submission{ProviderJobID: *record.ProviderJobID}, false)
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
		return w.ingest(ctx, record, result)
	case "failed":
		return w.fail(ctx, record, valueOr(result.ErrorCode, "PROVIDER_JOB_FAILED"), valueOr(result.ErrorText, "上游生成失败"), false)
	default:
		return river.JobSnooze(boundedSnooze(3*time.Second, record.GenerationDeadline))
	}
}

func (w *GenerateWorker) load(ctx context.Context, jobID uuid.UUID) (generationRecord, error) {
	var item generationRecord
	var snapshotJSON []byte
	var storedExpectedOutputs int
	err := w.DB.QueryRow(ctx, `SELECT j.id,j.batch_id,j.owner_user_id,j.status,COALESCE(j.cancel_mode,''),j.execution_generation,j.provider_job_id,j.expected_outputs,
		b.prompt,b.aspect_ratio,b.resolution,b.model_id,j.attempt_count,j.generation_deadline,j.upstream_active_until,v.config
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.id=$1`, jobID).Scan(
		&item.JobID, &item.BatchID, &item.OwnerID, &item.Status, &item.CancelMode, &item.ExecutionGeneration, &item.ProviderJobID, &storedExpectedOutputs,
		&item.Prompt, &item.AspectRatio, &item.Resolution, &item.ModelID, &item.AttemptCount, &item.GenerationDeadline, &item.UpstreamActiveUntil, &snapshotJSON)
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
	providerLimit := model.Policy.MaxConcurrency
	modelLimit := model.Policy.MaxConcurrency
	if modelLimit < 1 {
		modelLimit = 1
	}
	var providerActive, modelActive int
	if err = tx.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE v.config->>'provider'=$1)::int,
		count(*) FILTER (WHERE b.model_id=$2)::int
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.status IN ('submitting','provider_pending','cancelling') OR j.upstream_active_until>now()`, item.ProviderID, item.ModelID).Scan(&providerActive, &modelActive); err != nil {
		return submissionClaim{}, err
	}
	if providerActive >= providerLimit || modelActive >= modelLimit {
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
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.submitting',jsonb_build_object('status','submitting','attempt',$4))`, item.OwnerID, item.BatchID, item.JobID, attempt); err != nil {
		return submissionClaim{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return submissionClaim{}, err
	}
	return submissionClaim{Claimed: true, Attempt: attempt, Deadline: storedDeadline}, nil
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

// stageSynchronousResult durably associates an already-paid synchronous
// response with the business job before the job enters ingesting. Image bytes
// are committed to the immutable store and their metadata is committed in the
// same database transaction as the state transition.
func (w *GenerateWorker) stageSynchronousResult(ctx context.Context, item generationRecord, submission provider.Submission) (bool, error) {
	images := submission.Result.Images
	if len(images) == 0 {
		return false, &stagedResultError{code: "PROVIDER_EMPTY_RESULT", message: "上游没有返回图片"}
	}
	if providerOutputCountExceeded(len(images), item.ExpectedOutputs) {
		return false, &stagedResultError{code: "PROVIDER_OUTPUT_COUNT_INVALID", message: "上游返回的图片数量超过模型协议"}
	}
	type pendingStagedOutput struct {
		output    stagedGenerationOutput
		tempPath  string
		extension string
	}
	pending := make([]pendingStagedOutput, 0, len(images))
	defer func() {
		for _, candidate := range pending {
			if candidate.tempPath != "" {
				_ = os.Remove(candidate.tempPath)
			}
		}
	}()
	for index, output := range images {
		data := output.Bytes
		if len(data) == 0 && output.URL != "" {
			var err error
			data, output.MediaType, err = w.download(ctx, item, output.URL)
			if err != nil {
				return false, err
			}
		}
		media, extension, width, height, err := validateProviderImage(data)
		if err != nil {
			return false, &stagedResultError{code: "PROVIDER_IMAGE_INVALID", message: err.Error()}
		}
		tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", fmt.Sprintf("stage-%s-%d-%s.part", item.JobID, index, uuid.NewString()))
		if err = writeSynced(tempPath, data); err != nil {
			return false, err
		}
		pending = append(pending, pendingStagedOutput{
			tempPath: tempPath, extension: extension,
			output: stagedGenerationOutput{OutputIndex: index, MediaType: media, Width: width, Height: height},
		})
	}

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
		if _, err = tx.Exec(ctx, `INSERT INTO generation_staged_outputs(job_id,output_index,storage_key,sha256,media_type,width,height,byte_size)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (job_id,output_index) DO NOTHING`, item.JobID, output.OutputIndex, output.StorageKey, output.SHA256, output.MediaType, output.Width, output.Height, output.ByteSize); err != nil {
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
			WHERE id=$1 AND status='submitting'`, item.JobID, submission.ProviderJobID); err != nil {
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

func (w *GenerateWorker) stagedOutputCount(ctx context.Context, jobID uuid.UUID) (int, error) {
	var count int
	err := w.DB.QueryRow(ctx, `SELECT count(*)::int FROM generation_staged_outputs WHERE job_id=$1`, jobID).Scan(&count)
	return count, err
}

func (w *GenerateWorker) loadStagedOutputs(ctx context.Context, jobID uuid.UUID) ([]stagedGenerationOutput, error) {
	rows, err := w.DB.Query(ctx, `SELECT output_index,storage_key,sha256,media_type,width,height,byte_size
		FROM generation_staged_outputs WHERE job_id=$1 ORDER BY output_index`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outputs := make([]stagedGenerationOutput, 0)
	for rows.Next() {
		var output stagedGenerationOutput
		if err = rows.Scan(&output.OutputIndex, &output.StorageKey, &output.SHA256, &output.MediaType, &output.Width, &output.Height, &output.ByteSize); err != nil {
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
	command, err := tx.Exec(ctx, `UPDATE generation_jobs SET provider_job_id=NULLIF($2,''),status=$3,updated_at=now()
		WHERE id=$1 AND status='submitting' AND provider_job_id IS NULL`, item.JobID, submission.ProviderJobID, status)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.updated',jsonb_build_object('status',$4))`, item.OwnerID, item.BatchID, item.JobID, status); err != nil {
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
	return provider.CanonicalRequest{
		JobID:             item.JobID.String(),
		Model:             model.ProviderModel,
		Prompt:            prompt,
		AspectRatio:       item.AspectRatio,
		Resolution:        item.Resolution,
		ExpectedImages:    model.OutputsPerDraw,
		RequestParameters: append([]string(nil), model.RequestParameters...),
	}
}

func (w *GenerateWorker) handleProviderError(ctx context.Context, item generationRecord, model modelconfig.Model, err error, duringSubmit bool) error {
	var providerErr *provider.Error
	if !errors.As(err, &providerErr) {
		if duringSubmit {
			return w.markUncertain(ctx, item, "SUBMISSION_UNCERTAIN", "无法确认上游是否已接收任务，已停止自动重试", true)
		}
		return err
	}
	if providerErr.PauseProvider {
		if pauseErr := w.pauseProvider(ctx, item, model, providerErr); pauseErr != nil {
			return pauseErr
		}
	}
	if providerErr.SubmissionUncertain {
		return w.markUncertain(ctx, item, providerErr.Code, "提交结果不确定，已停止自动重试", true)
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
		error_code=$3,error_message=$4,retryable=true,updated_at=now() WHERE id=$1 AND status='submitting'`, item.JobID, pgInterval(delay), providerErr.Code, providerErr.Message)
	if updateErr != nil {
		return updateErr
	}
	if command.RowsAffected() != 1 {
		return river.JobSnooze(100 * time.Millisecond)
	}
	if _, updateErr = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.retry_scheduled',jsonb_build_object('status','dispatched','retry_after_ms',$4))`, item.OwnerID, item.BatchID, item.JobID, delay.Milliseconds()); updateErr != nil {
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

func (w *GenerateWorker) markUncertain(ctx context.Context, item generationRecord, code, message string, upstreamMayBeActive bool) error {
	deadline := uncertainStateDeadline(time.Now(), item, upstreamMayBeActive)
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
		return w.markCancelledInTx(ctx, tx, item, false, deadline)
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='submission_uncertain',submission_uncertain=true,error_code=$2,error_message=$3,
		dispatch_state='finished',upstream_active_until=$4,completed_at=now(),updated_at=now() WHERE id=$1`, item.JobID, code, message, deadline); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.submission_uncertain',jsonb_build_object('status','submission_uncertain','error_code',$4))`, item.OwnerID, item.BatchID, item.JobID, code); err != nil {
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

func (w *GenerateWorker) failWithUpstreamLease(ctx context.Context, item generationRecord, code, message string, retryable bool, upstreamActiveUntil *time.Time) error {
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
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.failed',jsonb_build_object('status','failed','error_code',$4,'message',$5,'retryable',$6,'upstream_may_still_be_active',$7::boolean))`, item.OwnerID, item.BatchID, item.JobID, code, message, retryable, upstreamActiveUntil != nil); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
			VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',$4,
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
		VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',$4,
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
	started := time.Now()
	result, err := adapter.Cancel(cancelCtx, submission)
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelRecord()
	w.recordAttempt(recordCtx, item, "cancel", time.Since(started), err, nil, result.Telemetry)
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
	pollCtx, cancelPoll := boundedContext(ctx, 45*time.Second, item.UpstreamActiveUntil)
	started := time.Now()
	result, pollErr := adapter.Poll(pollCtx, provider.Submission{ProviderJobID: *item.ProviderJobID})
	cancelPoll()
	release()
	w.recordPassiveBreaker(ctx, item, item.ModelSnapshot, item.ProviderID+":"+item.ModelID, pollErr)
	w.recordAttempt(ctx, item, "cancelled_poll", time.Since(started), pollErr, result.Usage, result.Telemetry)
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
	for _, output := range staged {
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
		if err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size)
			VALUES($1,'generation',$2,$3,$4,$5,$6,$7) RETURNING id`, item.OwnerID, output.StorageKey, output.SHA256, output.MediaType, output.Width, output.Height, output.ByteSize).Scan(&assetID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO generation_outputs(job_id,asset_id,output_index) VALUES($1,$2,$3)`, item.JobID, assetID, output.OutputIndex); err != nil {
			return err
		}
		newKeys = append(newKeys, output.StorageKey)
	}

	outputRows, err := tx.Query(ctx, `SELECT o.asset_id,o.output_index,a.width,a.height,a.media_type
		FROM generation_outputs o JOIN assets a ON a.id=o.asset_id WHERE o.job_id=$1 ORDER BY o.output_index`, item.JobID)
	if err != nil {
		return err
	}
	eventOutputs := make([]map[string]any, 0, item.ExpectedOutputs)
	for outputRows.Next() {
		var assetID uuid.UUID
		var outputIndex, width, height int
		var mediaType string
		if err = outputRows.Scan(&assetID, &outputIndex, &width, &height, &mediaType); err != nil {
			outputRows.Close()
			return err
		}
		eventOutputs = append(eventOutputs, map[string]any{
			"asset_id": assetID, "output_index": outputIndex, "width": width, "height": height, "media_type": mediaType,
			"thumb_320_url": "/api/v1/assets/" + assetID.String() + "/content?variant=320",
			"thumb_640_url": "/api/v1/assets/" + assetID.String() + "/content?variant=640",
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
	if _, err = tx.Exec(ctx, `DELETE FROM generation_staged_outputs WHERE job_id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='succeeded',dispatch_state='finished',completed_at=now(),error_code=NULL,error_message=NULL,updated_at=now() WHERE id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
		VALUES($1,$2,$3,'job.succeeded',jsonb_build_object('status','succeeded','output_count',$4,'outputs',$5::jsonb))`, item.OwnerID, item.BatchID, item.JobID, len(eventOutputs), string(encodedOutputs)); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, key := range newKeys {
		w.makeThumbnails(ctx, key)
	}
	return nil
}

func (w *GenerateWorker) ingest(ctx context.Context, item generationRecord, result provider.Result) error {
	select {
	case w.IngestSem <- struct{}{}:
		defer func() { <-w.IngestSem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if len(result.Images) == 0 {
		return w.fail(ctx, item, "PROVIDER_EMPTY_RESULT", "上游没有返回图片", safeForManualResubmit(item))
	}
	if providerOutputCountExceeded(len(result.Images), item.ExpectedOutputs) {
		return w.fail(ctx, item, "PROVIDER_OUTPUT_COUNT_INVALID", "上游返回的图片数量超过模型协议", false)
	}
	type prepared struct {
		tempPath         string
		extension, media string
		width, height    int
		key, digest      string
		size             int64
	}
	preparedImages := make([]prepared, 0, len(result.Images))
	defer func() {
		for _, output := range preparedImages {
			if output.tempPath != "" {
				_ = os.Remove(output.tempPath)
			}
		}
	}()
	for index, output := range result.Images {
		data := output.Bytes
		if len(data) == 0 && output.URL != "" {
			var err error
			data, output.MediaType, err = w.download(ctx, item, output.URL)
			if err != nil {
				return err
			}
		}
		media, extension, width, height, err := validateProviderImage(data)
		if err != nil {
			return w.fail(ctx, item, "PROVIDER_IMAGE_INVALID", err.Error(), false)
		}
		tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", fmt.Sprintf("%s-%d-%s.part", item.JobID, index, uuid.NewString()))
		if err := writeSynced(tempPath, data); err != nil {
			return err
		}
		preparedImages = append(preparedImages, prepared{tempPath: tempPath, extension: extension, media: media, width: width, height: height})
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
		if status == "cancelling" {
			if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='cancelled',dispatch_state='finished',completed_at=now(),updated_at=now() WHERE id=$1`, item.JobID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.cancelled',jsonb_build_object('status','cancelled','late_result_discarded',true))`, item.OwnerID, item.BatchID, item.JobID); err != nil {
				return err
			}
		}
		if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if status == "succeeded" {
		return tx.Commit(ctx)
	}
	if status != "ingesting" && status != "provider_pending" {
		return fmt.Errorf("cannot ingest generation job in status %s", status)
	}
	contentLease := w.Blobs.AcquireContentLease()
	leaseReleased := false
	defer func() {
		if !leaseReleased {
			contentLease.Release()
		}
	}()
	newKeys := make([]string, 0, len(preparedImages))
	for index := range preparedImages {
		output := &preparedImages[index]
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM generation_outputs WHERE job_id=$1 AND output_index=$2)`, item.JobID, index).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		output.key, output.digest, output.size, err = contentLease.PutImmutable(output.tempPath, output.extension)
		if err != nil {
			return err
		}
		output.tempPath = ""
		var assetID uuid.UUID
		if err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size) VALUES($1,'generation',$2,$3,$4,$5,$6,$7) RETURNING id`, item.OwnerID, output.key, output.digest, output.media, output.width, output.height, output.size).Scan(&assetID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO generation_outputs(job_id,asset_id,output_index) VALUES($1,$2,$3)`, item.JobID, assetID, index); err != nil {
			return err
		}
		newKeys = append(newKeys, output.key)
	}
	outputRows, err := tx.Query(ctx, `SELECT o.asset_id,o.output_index,a.width,a.height,a.media_type
		FROM generation_outputs o JOIN assets a ON a.id=o.asset_id WHERE o.job_id=$1 ORDER BY o.output_index`, item.JobID)
	if err != nil {
		return err
	}
	eventOutputs := make([]map[string]any, 0, item.ExpectedOutputs)
	for outputRows.Next() {
		var assetID uuid.UUID
		var outputIndex, width, height int
		var mediaType string
		if err = outputRows.Scan(&assetID, &outputIndex, &width, &height, &mediaType); err != nil {
			outputRows.Close()
			return err
		}
		eventOutputs = append(eventOutputs, map[string]any{
			"asset_id": assetID, "output_index": outputIndex, "width": width, "height": height, "media_type": mediaType,
			"thumb_320_url": "/api/v1/assets/" + assetID.String() + "/content?variant=320",
			"thumb_640_url": "/api/v1/assets/" + assetID.String() + "/content?variant=640",
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
	outputCount := len(eventOutputs)
	if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET status='succeeded',dispatch_state='finished',completed_at=now(),error_code=NULL,error_message=NULL,updated_at=now() WHERE id=$1`, item.JobID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.succeeded',jsonb_build_object('status','succeeded','output_count',$4,'outputs',$5::jsonb))`, item.OwnerID, item.BatchID, item.JobID, outputCount, string(encodedOutputs)); err != nil {
		return err
	}
	if _, err = reconcileWorkerBatch(ctx, tx, item.BatchID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	contentLease.Release()
	leaseReleased = true
	for _, key := range newKeys {
		w.makeThumbnails(ctx, key)
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
	if _, err := tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,event_type,payload) VALUES($1,$2,$3,jsonb_build_object('status',$4,'completed_outputs',$5))`, ownerID, batchID, eventType, status, completed); err != nil {
		return "", err
	}
	return status, nil
}

func (w *GenerateWorker) download(ctx context.Context, item generationRecord, rawURL string) ([]byte, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, "", &provider.Error{Code: "OUTPUT_URL_REJECTED", Message: "invalid output URL"}
	}
	allowed := false
	for _, host := range item.ModelSnapshot.Policy.AllowedOutputHosts {
		if strings.EqualFold(parsed.Hostname(), host) {
			allowed = true
		}
	}
	if !allowed {
		return nil, "", &provider.Error{Code: "OUTPUT_HOST_REJECTED", Message: "output host is not allowlisted"}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	res, err := w.HTTPClient.Do(req)
	if err != nil {
		// net/http wraps transport failures in url.Error, whose string contains
		// the complete provider URL. CDN query parameters can be bearer tokens,
		// so never let that error cross into River or structured logs.
		return nil, "", errors.New("provider result download failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, "", fmt.Errorf("download result: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 50*1024*1024+1))
	if err != nil || len(data) > 50*1024*1024 {
		return nil, "", errors.New("result image exceeds limit")
	}
	return data, res.Header.Get("Content-Type"), nil
}

func (w *GenerateWorker) makeThumbnails(ctx context.Context, key string) {
	select {
	case w.ThumbSem <- struct{}{}:
		defer func() { <-w.ThumbSem }()
	case <-ctx.Done():
		return
	}
	original, err := w.Blobs.Resolve(key)
	if err != nil {
		return
	}
	for _, size := range []string{"320", "640", "1280"} {
		final := filepath.Join(filepath.Dir(original), "thumb-"+size+".webp")
		if _, err := os.Stat(final); err == nil {
			continue
		}
		temp := final + ".part"
		thumbCtx, cancelThumb := context.WithTimeout(ctx, 60*time.Second)
		command := exec.CommandContext(thumbCtx, "vipsthumbnail", original, "--size", size+"x", "--output", temp+"[Q=82,strip]")
		command.Env = append(os.Environ(), "VIPS_CONCURRENCY=2", "VIPS_DISC_THRESHOLD=268435456", "MALLOC_ARENA_MAX=2")
		if output, err := command.CombinedOutput(); err != nil {
			cancelThumb()
			w.Log.Warn("thumbnail failed", "error", err, "detail", string(output), "asset", key)
			_ = os.Remove(temp)
			continue
		}
		cancelThumb()
		if err := os.Rename(temp, final); err != nil {
			if w.Log != nil {
				w.Log.Warn("thumbnail publish failed", "error", err, "asset", key, "variant", size)
			}
			_ = os.Remove(temp)
		}
	}
}

func (w *GenerateWorker) recordAttempt(ctx context.Context, item generationRecord, operation string, duration time.Duration, attemptErr error, usage map[string]any, telemetry provider.Telemetry) {
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
			if telemetry.HTTPStatus != 0 {
				message = fmt.Sprintf("provider returned HTTP %d", telemetry.HTTPStatus)
			}
		}
	}
	telemetry = telemetry.Normalized()
	usage = sanitizeAttemptUsage(usage)
	writeCtx, cancelWrite := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelWrite()
	_, err := w.DB.Exec(writeCtx, `INSERT INTO provider_attempts(job_id,provider_id,operation,attempt_no,provider_request_id,http_status,duration_ms,outcome,error_code,error_message,usage)
		VALUES($1,$2,$3,(SELECT count(*)+1 FROM provider_attempts WHERE job_id=$1 AND operation=$3),NULLIF($4,''),NULLIF($5,0),$6,$7,NULLIF($8,''),NULLIF($9,''),COALESCE($10,'{}'::jsonb))`, item.JobID, item.ProviderID, operation, telemetry.ProviderRequestID, telemetry.HTTPStatus, duration.Milliseconds(), outcome, code, message, usage)
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
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='degraded',breaker_open_until=$2,last_error_at=now(),updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID, until); err != nil && w.Log != nil {
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
	case "CONTENT_POLICY_REJECTED", "UNSUPPORTED_PARAMETER", "REFERENCE_URL_INVALID":
		return true
	}
	switch providerErr.Telemetry.HTTPStatus {
	case http.StatusBadRequest, http.StatusForbidden, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
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
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='degraded',breaker_open_until=$2,last_error_at=now(),updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID, until); err != nil && w.Log != nil {
			w.Log.Warn("provider passive breaker state update failed", "provider", item.ProviderID, "error", err)
		}
	} else if attemptErr == nil {
		if _, err := w.DB.Exec(ctx, `UPDATE providers SET state='healthy',breaker_open_until=NULL,last_probe_at=now(),last_error_code=NULL,updated_at=now() WHERE id=$1 AND state<>'paused'`, item.ProviderID); err != nil && w.Log != nil {
			w.Log.Warn("provider passive breaker recovery update failed", "provider", item.ProviderID, "error", err)
		}
	}
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
