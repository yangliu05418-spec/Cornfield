package httpapi

import (
	"context"
	"net/http"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const promptRefinementMetricTimeout = 3 * time.Second

// promptRefinementMetric deliberately contains no prompt, model output,
// digest, request hash or user identifier. The random ID is a write-only
// capability used by the browser to report aggregate outcome feedback.
type promptRefinementMetric struct {
	ID                 uuid.UUID
	ModelID            string
	PolicyVersion      string
	Outcome            string
	InputRunes         int
	OutputRunes        *int
	EditDistanceBucket *string
	LatencyMS          int
	Changed            bool
	PromptTokens       *int64
	CompletionTokens   *int64
	RiskCategories     []string
}

type promptRefinementMetricStore interface {
	Create(context.Context, promptRefinementMetric) error
	MarkUndone(context.Context, uuid.UUID) (bool, error)
	MarkSubmitted(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error)
}

type postgresPromptRefinementMetricStore struct {
	db *pgxpool.Pool
}

func (s *postgresPromptRefinementMetricStore) Create(ctx context.Context, metric promptRefinementMetric) error {
	_, err := s.db.Exec(ctx, `INSERT INTO prompt_refinement_metrics(
		id,model_id,policy_version,outcome,input_runes,output_runes,edit_distance_bucket,
		latency_ms,changed,prompt_tokens,completion_tokens,risk_categories
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		metric.ID, metric.ModelID, metric.PolicyVersion, metric.Outcome, metric.InputRunes,
		metric.OutputRunes, metric.EditDistanceBucket, metric.LatencyMS, metric.Changed,
		metric.PromptTokens, metric.CompletionTokens, metric.RiskCategories)
	return err
}

func (s *postgresPromptRefinementMetricStore) MarkUndone(ctx context.Context, refinementID uuid.UUID) (bool, error) {
	command, err := s.db.Exec(ctx, `UPDATE prompt_refinement_metrics
		SET undone_at=COALESCE(undone_at,now()) WHERE id=$1`, refinementID)
	return err == nil && command.RowsAffected() == 1, err
}

func (s *postgresPromptRefinementMetricStore) MarkSubmitted(ctx context.Context, refinementID, batchID, ownerID uuid.UUID) (bool, error) {
	command, err := s.db.Exec(ctx, `UPDATE prompt_refinement_metrics metric
		SET submitted_at=COALESCE(metric.submitted_at,now())
		WHERE metric.id=$1 AND EXISTS(
			SELECT 1 FROM generation_batches batch WHERE batch.id=$2 AND batch.owner_user_id=$3
		)`, refinementID, batchID, ownerID)
	return err == nil && command.RowsAffected() == 1, err
}

func (s *Server) createPromptRefinementMetric(metric promptRefinementMetric) error {
	if s.promptRefinementMetrics == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), promptRefinementMetricTimeout)
	defer cancel()
	return s.promptRefinementMetrics.Create(ctx, metric)
}

func (s *Server) promptRefinementFeedback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	refinementID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Event   string     `json:"event"`
		BatchID *uuid.UUID `json:"batch_id,omitempty"`
	}
	if !decodeJSONLimited(w, r, &input, 4<<10) {
		return
	}
	if s.promptRefinementMetrics == nil {
		writeError(w, http.StatusServiceUnavailable, "PROMPT_REFINER_METRICS_UNAVAILABLE", "提示词优化反馈暂不可用", true, r)
		return
	}

	var updated bool
	var err error
	switch input.Event {
	case "undone":
		if input.BatchID != nil {
			writeError(w, http.StatusUnprocessableEntity, "REFINEMENT_FEEDBACK_INVALID", "撤销反馈不能包含生成批次", false, r)
			return
		}
		updated, err = s.promptRefinementMetrics.MarkUndone(r.Context(), refinementID)
	case "submitted":
		if input.BatchID == nil {
			writeError(w, http.StatusUnprocessableEntity, "REFINEMENT_FEEDBACK_INVALID", "提交反馈需要生成批次", false, r)
			return
		}
		updated, err = s.promptRefinementMetrics.MarkSubmitted(r.Context(), refinementID, *input.BatchID, currentSession(r).UserID)
	default:
		writeError(w, http.StatusUnprocessableEntity, "REFINEMENT_FEEDBACK_INVALID", "反馈类型无效", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REFINEMENT_FEEDBACK_FAILED", "提示词优化反馈暂未保存，请稍后重试", true, r)
		return
	}
	if !updated {
		// The refinement ID is an unguessable write capability. Use the same
		// response for a missing metric and an unowned generation batch.
		writeError(w, http.StatusNotFound, "REFINEMENT_FEEDBACK_NOT_FOUND", "优化记录或生成批次不存在", false, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func promptRiskCategories(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func promptMetricTokenCount(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func promptMetricOutputRunes(value string) *int {
	count := utf8.RuneCountInString(value)
	return &count
}

// The existing linear-time bigram similarity is used as a bounded edit
// magnitude estimate. Exact rune Levenshtein is quadratic for the supported
// 32,768-character input and is inappropriate on the request path.
func promptEditDistanceBucket(original, candidate string) *string {
	left, right := []rune(original), []rune(candidate)
	if string(left) == string(right) {
		value := "none"
		return &value
	}
	distance := 1 - promptBigramSimilarity(left, right)
	value := "over_45"
	switch {
	case distance <= 0.10:
		value = "0_10"
	case distance <= 0.25:
		value = "10_25"
	case distance <= 0.45:
		value = "25_45"
	}
	return &value
}
