package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Server) listSubmissionUncertain(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "分页大小必须在 1 到 100 之间", false, r)
			return
		}
		limit = parsed
	}
	cursorTime, cursorID, err := decodeGenerationCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "分页游标无效", false, r)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT j.id,j.batch_id,u.id,u.username::text,b.model_id,v.config->>'provider',j.created_at,j.provider_job_id,
		a.operation,a.outcome,a.error_code,a.error_message,a.http_status,a.finished_at
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN users u ON u.id=j.owner_user_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		LEFT JOIN LATERAL (
			SELECT operation,outcome,error_code,error_message,http_status,finished_at
			FROM provider_attempts WHERE job_id=j.id ORDER BY id DESC LIMIT 1
		) a ON true
		WHERE j.status='submission_uncertain'
		  AND ($1::timestamptz IS NULL OR (j.created_at,j.id)<($1,$2::uuid))
		ORDER BY j.created_at DESC,j.id DESC LIMIT $3`, cursorTime, cursorID, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "无法读取需要核查的提交", true, r)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit+1)
	var lastCreated time.Time
	var lastID uuid.UUID
	for rows.Next() {
		var jobID, batchID, userID uuid.UUID
		var username, modelID, providerID string
		var createdAt time.Time
		var providerJobID, operation, outcome, errorCode, errorMessage *string
		var httpStatus *int
		var finishedAt *time.Time
		if err = rows.Scan(&jobID, &batchID, &userID, &username, &modelID, &providerID, &createdAt, &providerJobID, &operation, &outcome, &errorCode, &errorMessage, &httpStatus, &finishedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "无法读取需要核查的提交", true, r)
			return
		}
		items = append(items, map[string]any{
			"id": jobID, "batch_id": batchID, "user_id": userID, "username": username,
			"model_id": modelID, "provider_id": providerID, "created_at": createdAt,
			"age_seconds": max(int(time.Since(createdAt).Seconds()), 0), "provider_job_id": providerJobID,
			"latest_attempt": map[string]any{"operation": operation, "outcome": outcome, "error_code": errorCode, "error_message": errorMessage, "http_status": httpStatus, "finished_at": finishedAt},
		})
		lastCreated, lastID = createdAt, jobID
	}
	if err = rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "无法读取需要核查的提交", true, r)
		return
	}
	nextCursor := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		lastCreated = last["created_at"].(time.Time)
		lastID = last["id"].(uuid.UUID)
		nextCursor = encodeGenerationCursor(lastCreated, lastID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

const (
	maxProviderJobIDBytes       = 256
	maxReconciliationPollWindow = 24 * time.Hour
)

var (
	errReconciliationState      = errors.New("job is not awaiting submission reconciliation")
	errReconciliationAction     = errors.New("unsupported submission reconciliation action")
	errProviderJobID            = errors.New("provider job id is invalid")
	errRemoteAbsenceUnconfirmed = errors.New("remote absence was not explicitly confirmed")
	errAcceptedLossUnconfirmed  = errors.New("accepted unrecoverable result was not explicitly confirmed")
)

type reconcileSubmissionInput struct {
	Action                       string `json:"action"`
	ProviderJobID                string `json:"provider_job_id"`
	ConfirmedRemoteAbsent        bool   `json:"confirmed_remote_absent"`
	ConfirmedProviderAccepted    bool   `json:"confirmed_provider_accepted"`
	ConfirmedResultUnrecoverable bool   `json:"confirmed_result_unrecoverable"`
}

type submissionReconciliationPlan struct {
	Action                string
	Status                string
	ProviderJobID         *string
	GenerationDeadline    *time.Time
	UpstreamActiveUntil   *time.Time
	ResetAttempts         bool
	Retryable             bool
	DuplicateCostRisk     bool
	ProviderChargeAssumed bool
	NextOperation         string
}

func planSubmissionReconciliation(currentStatus string, input reconcileSubmissionInput, now time.Time, pollWindow time.Duration) (submissionReconciliationPlan, error) {
	if currentStatus != "submission_uncertain" {
		return submissionReconciliationPlan{}, errReconciliationState
	}
	switch input.Action {
	case "attach_provider_job":
		if input.ConfirmedRemoteAbsent || input.ConfirmedProviderAccepted || input.ConfirmedResultUnrecoverable {
			return submissionReconciliationPlan{}, errReconciliationAction
		}
		providerJobID := strings.TrimSpace(input.ProviderJobID)
		if !validProviderJobID(providerJobID) {
			return submissionReconciliationPlan{}, errProviderJobID
		}
		if pollWindow <= 0 {
			pollWindow = 15 * time.Minute
		}
		if pollWindow > maxReconciliationPollWindow {
			pollWindow = maxReconciliationPollWindow
		}
		deadline := now.Add(pollWindow)
		return submissionReconciliationPlan{
			Action:              input.Action,
			Status:              "queued",
			ProviderJobID:       &providerJobID,
			GenerationDeadline:  &deadline,
			UpstreamActiveUntil: &deadline,
			Retryable:           true,
			NextOperation:       "poll_provider",
		}, nil
	case "confirm_absent":
		if strings.TrimSpace(input.ProviderJobID) != "" || input.ConfirmedProviderAccepted || input.ConfirmedResultUnrecoverable {
			return submissionReconciliationPlan{}, errReconciliationAction
		}
		if !input.ConfirmedRemoteAbsent {
			return submissionReconciliationPlan{}, errRemoteAbsenceUnconfirmed
		}
		return submissionReconciliationPlan{
			Action:            input.Action,
			Status:            "queued",
			ResetAttempts:     true,
			Retryable:         true,
			DuplicateCostRisk: true,
			NextOperation:     "submit_provider",
		}, nil
	case "confirm_accepted_unrecoverable":
		if strings.TrimSpace(input.ProviderJobID) != "" || input.ConfirmedRemoteAbsent || !input.ConfirmedProviderAccepted || !input.ConfirmedResultUnrecoverable {
			return submissionReconciliationPlan{}, errAcceptedLossUnconfirmed
		}
		return submissionReconciliationPlan{
			Action:                input.Action,
			Status:                "failed",
			ProviderChargeAssumed: true,
			NextOperation:         "none",
		}, nil
	default:
		return submissionReconciliationPlan{}, errReconciliationAction
	}
}

func validProviderJobID(value string) bool {
	if value == "" || len(value) > maxProviderJobIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func providerSupportsPolling(providerID string) bool {
	// BFL polling requires the cluster-specific URL returned at submission;
	// the reconciliation endpoint currently accepts only a remote job ID.
	return providerID == "legnext"
}

func reconciliationPollWindow(seconds int) time.Duration {
	if seconds < 1 {
		return 15 * time.Minute
	}
	if seconds > int(maxReconciliationPollWindow/time.Second) {
		return maxReconciliationPollWindow
	}
	window := time.Duration(seconds) * time.Second
	if window > maxReconciliationPollWindow {
		return maxReconciliationPollWindow
	}
	return window
}

func (s *Server) reconcileSubmission(w http.ResponseWriter, r *http.Request) {
	jobID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input reconcileSubmissionInput
	if !decodeJSON(w, r, &input) {
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to start submission reconciliation", true, r)
		return
	}
	defer tx.Rollback(r.Context())

	// Lock the batch before the job, matching the rest of the generation state
	// machine and avoiding an inverted lock order with cancel/retry operations.
	var batchID uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT batch_id FROM generation_jobs WHERE id=$1`, jobID).Scan(&batchID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Generation job not found", false, r)
		} else {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to read generation job", true, r)
		}
		return
	}
	var ownerID uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT owner_user_id FROM generation_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&ownerID); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to lock generation batch", true, r)
		return
	}
	var currentStatus string
	var previousProviderJobID *string
	var generationTimeoutSeconds int
	var providerID string
	if err = tx.QueryRow(r.Context(), `SELECT j.status,j.provider_job_id,
		v.config->>'provider',
		COALESCE(NULLIF(v.config #>> '{policy,generation_timeout_seconds}','')::int,
			NULLIF(v.config #>> '{policy,GenerationTimeoutSeconds}','')::int,900)
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.id=$1 FOR UPDATE OF j`, jobID).Scan(&currentStatus, &previousProviderJobID, &providerID, &generationTimeoutSeconds); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "JOB_NOT_FOUND", "Generation job not found", false, r)
		} else {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to lock generation job", true, r)
		}
		return
	}
	plan, planErr := planSubmissionReconciliation(currentStatus, input, time.Now().UTC(), reconciliationPollWindow(generationTimeoutSeconds))
	if planErr == nil && plan.Action == "attach_provider_job" && !providerSupportsPolling(providerID) {
		writeError(w, http.StatusUnprocessableEntity, "PROVIDER_POLL_UNSUPPORTED", "This provider has no asynchronous poll API; use confirm_accepted_unrecoverable when a charged result cannot be recovered", false, r)
		return
	}
	if planErr != nil {
		switch {
		case errors.Is(planErr, errReconciliationState):
			writeError(w, http.StatusConflict, "JOB_RECONCILIATION_CONFLICT", "Only submission_uncertain jobs can be reconciled", false, r)
		case errors.Is(planErr, errProviderJobID):
			writeError(w, http.StatusUnprocessableEntity, "PROVIDER_JOB_ID_INVALID", "provider_job_id must be non-empty, at most 256 bytes, and contain no whitespace or control characters", false, r)
		case errors.Is(planErr, errRemoteAbsenceUnconfirmed):
			writeError(w, http.StatusUnprocessableEntity, "REMOTE_ABSENCE_CONFIRMATION_REQUIRED", "Set confirmed_remote_absent=true only after verifying that the provider did not create a remote job", false, r)
		case errors.Is(planErr, errAcceptedLossUnconfirmed):
			writeError(w, http.StatusUnprocessableEntity, "ACCEPTED_LOSS_CONFIRMATION_REQUIRED", "Set confirmed_provider_accepted=true and confirmed_result_unrecoverable=true only after verifying that the charged result cannot be recovered", false, r)
		default:
			writeError(w, http.StatusUnprocessableEntity, "RECONCILIATION_ACTION_INVALID", "action must be attach_provider_job, confirm_absent, or confirm_accepted_unrecoverable", false, r)
		}
		return
	}
	if plan.Action == "confirm_absent" {
		if err = lockUsableBatchInputAssets(r.Context(), tx, ownerID, batchID); err != nil {
			if errors.Is(err, errGenerationReferenceUnavailable) {
				writeError(w, http.StatusConflict, "REFERENCE_UNAVAILABLE", "A reference image has expired or is being removed; a new provider submission is not safe", false, r)
			} else {
				writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to validate generation references", true, r)
			}
			return
		}
	}

	var rowsAffected int64
	if plan.Status == "failed" {
		var command pgconn.CommandTag
		command, err = tx.Exec(r.Context(), `UPDATE generation_jobs SET status='failed',dispatch_state='finished',river_job_id=NULL,
			submission_uncertain=false,retryable=false,generation_deadline=NULL,upstream_active_until=NULL,completed_at=now(),cancel_mode=NULL,
			error_code='PROVIDER_RESULT_UNRECOVERABLE',error_message='生成失败，请稍后重试',updated_at=now()
			WHERE id=$1 AND status='submission_uncertain'`, jobID)
		rowsAffected = command.RowsAffected()
	} else if plan.ProviderJobID != nil {
		var command pgconn.CommandTag
		command, err = tx.Exec(r.Context(), `UPDATE generation_jobs SET status='queued',dispatch_state='pending',river_job_id=NULL,
			provider_job_id=$2,submission_uncertain=false,retryable=true,next_attempt_at=now(),generation_deadline=$3,
			upstream_active_until=$3,execution_generation=execution_generation+1,dispatched_at=NULL,completed_at=NULL,cancel_mode=NULL,error_code=NULL,error_message=NULL,dismissed_at=NULL,updated_at=now()
			WHERE id=$1 AND status='submission_uncertain'`, jobID, *plan.ProviderJobID, *plan.UpstreamActiveUntil)
		rowsAffected = command.RowsAffected()
	} else {
		var command pgconn.CommandTag
		command, err = tx.Exec(r.Context(), `UPDATE generation_jobs SET status='queued',dispatch_state='pending',river_job_id=NULL,
			provider_job_id=NULL,submission_uncertain=false,retryable=true,next_attempt_at=now(),generation_deadline=NULL,upstream_active_until=NULL,
			attempt_count=0,execution_generation=execution_generation+1,dispatched_at=NULL,started_at=NULL,completed_at=NULL,cancel_mode=NULL,error_code=NULL,error_message=NULL,dismissed_at=NULL,updated_at=now()
			WHERE id=$1 AND status='submission_uncertain'`, jobID)
		rowsAffected = command.RowsAffected()
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to reconcile generation job", true, r)
		return
	}
	if rowsAffected != 1 {
		writeError(w, http.StatusConflict, "JOB_RECONCILIATION_CONFLICT", "Generation job state changed during reconciliation", false, r)
		return
	}

	eventType := "job.submission_reconciled"
	if plan.Status == "failed" {
		eventType = "job.failed"
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
		VALUES($1,$2,$3,$4,jsonb_build_object('status',$5::text,'action',$6::text,'next_operation',$7::text,
		'duplicate_cost_risk',$8::boolean,'provider_charge_assumed',$9::boolean,'retryable',$10::boolean,
		'error_code',CASE WHEN $5::text='failed' THEN 'PROVIDER_RESULT_UNRECOVERABLE' ELSE NULL END))`,
		ownerID, batchID, jobID, eventType, plan.Status, plan.Action, plan.NextOperation,
		plan.DuplicateCostRisk, plan.ProviderChargeAssumed, plan.Retryable); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to record reconciliation event", true, r)
		return
	}
	requestID := requestIDFromContext(r)
	var attachedProviderJobID any
	if plan.ProviderJobID != nil {
		attachedProviderJobID = *plan.ProviderJobID
	}
	var priorProviderJobID any
	if previousProviderJobID != nil {
		priorProviderJobID = *previousProviderJobID
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,request_id,metadata)
		VALUES($1,$2,'generation_job',$3,$4,jsonb_build_object('batch_id',$5::uuid,'provider_job_id',$6::text,
		'previous_provider_job_id',$7::text,'duplicate_cost_risk',$8::boolean,'confirmed_remote_absent',$9::boolean,
		'confirmed_provider_accepted',$10::boolean,'confirmed_result_unrecoverable',$11::boolean,'provider_charge_assumed',$12::boolean))`,
		currentSession(r).UserID, "job.reconcile_submission."+plan.Action, jobID.String(), requestID, batchID,
		attachedProviderJobID, priorProviderJobID, plan.DuplicateCostRisk, input.ConfirmedRemoteAbsent,
		input.ConfirmedProviderAccepted, input.ConfirmedResultUnrecoverable, plan.ProviderChargeAssumed); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to record reconciliation audit", true, r)
		return
	}
	batchStatus, err := reconcileHTTPBatch(r.Context(), tx, batchID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to reconcile generation batch", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "Unable to commit submission reconciliation", true, r)
		return
	}

	response := map[string]any{
		"job_id":                  jobID,
		"batch_id":                batchID,
		"status":                  plan.Status,
		"batch_status":            batchStatus,
		"action":                  plan.Action,
		"next_operation":          plan.NextOperation,
		"duplicate_cost_risk":     plan.DuplicateCostRisk,
		"provider_charge_assumed": plan.ProviderChargeAssumed,
		"retryable":               plan.Retryable,
	}
	if plan.ProviderJobID != nil {
		response["provider_job_id"] = *plan.ProviderJobID
	} else if plan.Status == "queued" {
		response["warning"] = "A new provider submission may duplicate cost if the remote absence check was wrong"
	} else {
		response["warning"] = "The provider charge is retained as an operational loss; this job cannot be retried"
	}
	writeJSON(w, http.StatusAccepted, response)
}
