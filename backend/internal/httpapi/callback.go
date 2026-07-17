package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"internal-image-studio/internal/providercallback"
)

const (
	maxLegnextCallbackBody   = 64 << 10
	maxLegnextCallbackEvents = 32
	legnextCallbackGrace     = 15 * time.Minute
	legacyCallbackLifetime   = 24 * time.Hour
)

type legnextCallbackPayload struct {
	JobID  string `json:"job_id"`
	TaskID string `json:"task_id"`
	ID     string `json:"id"`
	Status string `json:"status"`
	Data   *struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"`
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"data"`
}

type normalizedLegnextCallback struct {
	ProviderJobID string
	Status        string
}

func (s *Server) legnextCallback(w http.ResponseWriter, r *http.Request) {
	jobID, err := uuid.Parse(r.PathValue("jobID"))
	if err != nil || s.cfg.ProviderCallbackSecret == "" || !providercallback.Verify(s.cfg.ProviderCallbackSecret, jobID, r.PathValue("signature")) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLegnextCallbackBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "CALLBACK_TOO_LARGE", "回调内容超过大小限制", false, r)
			return
		}
		writeError(w, http.StatusBadRequest, "CALLBACK_INVALID", "回调内容无效", false, r)
		return
	}
	callback, err := decodeLegnextCallback(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "CALLBACK_INVALID", "回调内容无效", false, r)
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALLBACK_STORE_FAILED", "回调暂时无法保存", true, r)
		return
	}
	defer tx.Rollback(r.Context())

	var expectedProviderJobID *string
	var status, providerID string
	var generationDeadline *time.Time
	var createdAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT j.provider_job_id,j.status,j.generation_deadline,j.created_at,
		COALESCE(v.config->>'provider','')
		FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id
		JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		WHERE j.id=$1
		FOR UPDATE OF j`, jobID).Scan(&expectedProviderJobID, &status, &generationDeadline, &createdAt, &providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CALLBACK_STORE_FAILED", "回调暂时无法保存", true, r)
		return
	}
	if providerID != "legnext" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if expectedProviderJobID != nil {
		if callback.ProviderJobID != "" && callback.ProviderJobID != *expectedProviderJobID {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		callback.ProviderJobID = *expectedProviderJobID
	} else {
		// A callback is only a wake-up signal. Never persist an identifier from
		// an unverified callback before the authenticated submit response has
		// established the provider job ID.
		callback.ProviderJobID = ""
	}
	if !callbackJobIsActive(status) || callbackExpired(time.Now(), generationDeadline, createdAt) {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	var eventCount int
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM provider_callback_events WHERE generation_job_id=$1`, jobID).Scan(&eventCount); err != nil {
		writeError(w, http.StatusInternalServerError, "CALLBACK_STORE_FAILED", "回调暂时无法保存", true, r)
		return
	}
	if eventCount >= maxLegnextCallbackEvents {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	digestInput := append([]byte(jobID.String()+"\n"), body...)
	digest := sha256.Sum256(digestInput)
	minimalPayload := map[string]string{"status": callback.Status}
	command, err := tx.Exec(r.Context(), `INSERT INTO provider_callback_events(provider_id,event_hash,generation_job_id,provider_job_id,payload)
		VALUES('legnext',$1,$2,NULLIF($3,''),$4) ON CONFLICT DO NOTHING`, hex.EncodeToString(digest[:]), jobID, callback.ProviderJobID, minimalPayload)
	if err == nil && command.RowsAffected() > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE generation_jobs SET next_attempt_at=now(),updated_at=now() WHERE id=$1 AND status='provider_pending'`, jobID)
		if err == nil {
			_, err = tx.Exec(r.Context(), `SELECT pg_notify('provider_callbacks',$1)`, jobID.String())
		}
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "CALLBACK_STORE_FAILED", "回调暂时无法保存", true, r)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func decodeLegnextCallback(body []byte) (normalizedLegnextCallback, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" || trimmed[0] != '{' {
		return normalizedLegnextCallback{}, errors.New("callback must be a JSON object")
	}
	var payload legnextCallbackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return normalizedLegnextCallback{}, err
	}
	identifiers := []string{payload.JobID, payload.TaskID, payload.ID}
	status := payload.Status
	if payload.Data != nil {
		identifiers = append(identifiers, payload.Data.JobID, payload.Data.TaskID, payload.Data.ID)
		if status == "" {
			status = payload.Data.Status
		}
	}
	providerJobID := ""
	for _, candidate := range identifiers {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if !validCallbackToken(candidate, 256) {
			return normalizedLegnextCallback{}, errors.New("invalid provider job id")
		}
		providerJobID = candidate
		break
	}
	return normalizedLegnextCallback{ProviderJobID: providerJobID, Status: normalizeCallbackStatus(status)}, nil
}

func validCallbackToken(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func normalizeCallbackStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "queued", "pending", "processing", "running", "completed", "succeeded", "failed", "canceled", "cancelled":
		return status
	default:
		return "unknown"
	}
}

func callbackJobIsActive(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled":
		return false
	default:
		return true
	}
}

func callbackExpired(now time.Time, generationDeadline *time.Time, createdAt time.Time) bool {
	cutoff := createdAt.Add(legacyCallbackLifetime)
	if generationDeadline != nil {
		cutoff = generationDeadline.Add(legnextCallbackGrace)
	}
	return now.After(cutoff)
}
