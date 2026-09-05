package httpapi

import (
	"context"
	"math"
)

type generationWait struct {
	QueuedDraws  int `json:"queued_draws"`
	LowerSeconds int `json:"lower_seconds"`
	UpperSeconds int `json:"upper_seconds"`
	SampleSize   int `json:"sample_size"`
}

// Bounded successful execution samples exclude queue time so queue pressure is
// not counted twice. This is an estimate for a new request, not a reserved slot.
func (s *Server) generationWaits(ctx context.Context) (map[string]generationWait, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id,m.provider_id,
		COALESCE(q.queued,0),COALESCE(q.active,0),stats.samples,COALESCE(stats.p50,0),COALESCE(stats.p90,0)
		FROM models m
		LEFT JOIN LATERAL (SELECT count(*) FILTER (WHERE j.status IN ('queued','dispatched')) queued,
		count(*) FILTER (WHERE j.status IN ('submitting','provider_pending','cancelling')) active
		FROM generation_jobs j JOIN generation_batches b ON b.id=j.batch_id
		JOIN models qm ON qm.id=b.model_id WHERE qm.provider_id=m.provider_id
		AND j.status IN ('queued','dispatched','submitting','provider_pending','cancelling')) q ON true
		CROSS JOIN LATERAL (SELECT count(*) samples,percentile_cont(0.5) WITHIN GROUP(ORDER BY seconds) p50,
		percentile_cont(0.9) WITHIN GROUP(ORDER BY seconds) p90 FROM
		(SELECT extract(epoch FROM j.completed_at-j.started_at)::float8 seconds FROM generation_jobs j
		JOIN generation_batches b ON b.id=j.batch_id WHERE b.model_id=m.id AND j.status='succeeded'
		AND j.started_at IS NOT NULL AND j.completed_at>j.started_at AND j.created_at>now()-interval '30 days'
		ORDER BY j.created_at DESC LIMIT 100) sample) stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]generationWait)
	for rows.Next() {
		var id, providerID string
		var queued, active, samples int
		var p50, p90 float64
		if err := rows.Scan(&id, &providerID, &queued, &active, &samples, &p50, &p90); err != nil {
			return nil, err
		}
		for _, model := range s.catalog.Models {
			if model.ID != id || !model.Enabled {
				continue
			}
			result[id] = estimateGenerationWait(queued, active, samples, model.Policy.MaxConcurrency, p50, p90)
		}
	}
	return result, rows.Err()
}

func estimateGenerationWait(queued, active, samples, limit int, p50, p90 float64) generationWait {
	wait := generationWait{QueuedDraws: queued, SampleSize: samples}
	if samples < 5 || limit < 1 {
		return wait
	}
	waves := 1 + (queued+active)/limit
	wait.LowerSeconds = int(math.Floor(p50/5) * 5)
	wait.UpperSeconds = int(math.Ceil(p90*float64(waves)/5) * 5)
	return wait
}
