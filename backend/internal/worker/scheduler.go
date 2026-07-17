package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"internal-image-studio/internal/diskspace"
)

const schedulerLockID int64 = 4919348247201

var errSchedulerLeadershipUnavailable = errors.New("scheduler leadership unavailable")

type Scheduler struct {
	DB        *pgxpool.Pool
	River     *river.Client[pgx.Tx]
	Log       *slog.Logger
	Wake      chan struct{}
	AssetRoot string
}

func (s *Scheduler) Run(ctx context.Context) {
	retryDelay := 500 * time.Millisecond
	for ctx.Err() == nil {
		err := s.runAsLeader(ctx)
		if ctx.Err() != nil {
			return
		}
		s.Log.Warn("fair scheduler leadership lost; retrying", "error", err, "retry_after", retryDelay)
		timer := time.NewTimer(retryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if retryDelay < 5*time.Second {
			retryDelay *= 2
			if retryDelay > 5*time.Second {
				retryDelay = 5 * time.Second
			}
		}
	}
}

func (s *Scheduler) runAsLeader(ctx context.Context) error {
	lockConn, err := s.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lockConn.Release()
	var locked bool
	if err := lockConn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, schedulerLockID).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return errSchedulerLeadershipUnavailable
	}
	s.Log.Info("fair scheduler leadership acquired")
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = lockConn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, schedulerLockID)
	}()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// The advisory lock is session-scoped. Touch the owning connection
			// before dispatch so a dead PostgreSQL session cannot leave this
			// process dispatching without leadership.
			var alive int
			if err := lockConn.QueryRow(ctx, `SELECT 1`).Scan(&alive); err != nil {
				return err
			}
			s.dispatch(ctx)
		case <-s.Wake:
			var alive int
			if err := lockConn.QueryRow(ctx, `SELECT 1`).Scan(&alive); err != nil {
				return err
			}
			s.dispatch(ctx)
		}
	}
}

// ListenNotifications turns durable PostgreSQL notifications into River
// control operations. The business rows remain the source of truth; a missed
// notification is harmless because normal polling and scheduler ticks remain.
func (s *Scheduler) ListenNotifications(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := s.DB.Acquire(ctx)
		if err != nil {
			s.waitReconnect(ctx)
			continue
		}
		_, callbackErr := conn.Exec(ctx, "LISTEN provider_callbacks")
		_, controlErr := conn.Exec(ctx, "LISTEN job_controls")
		if callbackErr != nil || controlErr != nil {
			conn.Release()
			s.waitReconnect(ctx)
			continue
		}
		for ctx.Err() == nil {
			notification, waitErr := conn.Conn().WaitForNotification(ctx)
			if waitErr != nil {
				break
			}
			jobID, parseErr := uuid.Parse(notification.Payload)
			if parseErr != nil {
				continue
			}
			var riverJobID *int64
			var status string
			var upstreamActiveUntil *time.Time
			if queryErr := s.DB.QueryRow(ctx, `SELECT river_job_id,status,upstream_active_until FROM generation_jobs WHERE id=$1`, jobID).Scan(&riverJobID, &status, &upstreamActiveUntil); queryErr != nil || riverJobID == nil {
				continue
			}
			trackedUpstream := upstreamLeaseActive(time.Now(), upstreamActiveUntil)
			switch notification.Channel {
			case "provider_callbacks":
				if status == "provider_pending" || trackedUpstream {
					if _, retryErr := s.River.JobRetry(ctx, *riverJobID); retryErr != nil {
						s.Log.Debug("callback could not wake River job", "error", retryErr, "generation_job_id", jobID)
					}
				}
			case "job_controls":
				if status == "cancelling" || trackedUpstream {
					if _, retryErr := s.River.JobRetry(ctx, *riverJobID); retryErr != nil {
						s.Log.Debug("cancel could not wake River job", "error", retryErr, "generation_job_id", jobID)
					}
				} else if status == "cancelled" {
					if _, cancelErr := s.River.JobCancel(ctx, *riverJobID); cancelErr != nil {
						s.Log.Debug("River job already finalized during cancel", "error", cancelErr, "generation_job_id", jobID)
					}
				}
			}
		}
		conn.Release()
		s.waitReconnect(ctx)
	}
}

func (s *Scheduler) waitReconnect(ctx context.Context) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (s *Scheduler) dispatch(ctx context.Context) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		s.Log.Warn("scheduler transaction failed", "error", err)
		return
	}
	defer tx.Rollback(ctx)
	if err := s.recoverStagedIngests(ctx, tx); err != nil {
		s.Log.Warn("staged ingest recovery scheduling failed", "error", err)
		return
	}
	free, diskErr := diskspace.FreePercent(s.AssetRoot)
	if diskErr != nil {
		// A paid submission must not start when the Worker cannot prove that the
		// result volume is writable and has capacity. Staged recovery remains
		// safe to commit because its bytes are already durable.
		if commitErr := tx.Commit(ctx); commitErr != nil {
			s.Log.Warn("staged recovery commit failed", "error", commitErr)
			return
		}
		s.Log.Error("provider dispatch paused because disk capacity is unknown", "error", diskErr)
		return
	}
	if free < 10 {
		// Result bytes are already durable, so recovery remains allowed under
		// disk pressure while new paid provider submissions remain paused.
		if commitErr := tx.Commit(ctx); commitErr != nil {
			s.Log.Warn("staged recovery commit failed", "error", commitErr)
			return
		}
		s.Log.Warn("provider dispatch paused by critical disk pressure", "free_percent", free)
		return
	}
	rows, err := tx.Query(ctx, `
		WITH active AS (
			SELECT owner_user_id,count(*)::int AS count FROM generation_jobs
			WHERE status IN ('dispatched','submitting','provider_pending','ingesting','cancelling')
			   OR (upstream_active_until>now() AND status<>'queued')
			GROUP BY owner_user_id
		), candidates AS (
			SELECT j.id,j.owner_user_id,j.created_at,j.provider_job_id,j.execution_generation,
			       row_number() OVER(PARTITION BY j.owner_user_id ORDER BY (j.provider_job_id IS NOT NULL) DESC,j.created_at,j.id) AS user_rank,
			       COALESCE(a.count,0) AS active_count
			FROM generation_jobs j
			JOIN generation_batches b ON b.id=j.batch_id
			JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
			JOIN providers p ON p.id=v.config->>'provider'
			LEFT JOIN active a ON a.owner_user_id=j.owner_user_id
			WHERE j.dispatch_state='pending' AND j.status='queued' AND j.next_attempt_at<=now()
			  AND (j.provider_job_id IS NOT NULL OR (p.enabled=true AND p.state<>'paused'))
		)
		SELECT id,provider_job_id,execution_generation FROM candidates
		WHERE provider_job_id IS NOT NULL OR user_rank <= GREATEST(0,4-active_count)
		ORDER BY user_rank,(provider_job_id IS NOT NULL) DESC,created_at LIMIT 64`)
	if err != nil {
		s.Log.Warn("scheduler candidate query failed", "error", err)
		return
	}
	type candidate struct {
		id                  uuid.UUID
		providerJobID       *string
		executionGeneration int
	}
	candidates := make([]candidate, 0, 64)
	for rows.Next() {
		var item candidate
		if scanErr := rows.Scan(&item.id, &item.providerJobID, &item.executionGeneration); scanErr != nil {
			rows.Close()
			s.Log.Warn("scheduler candidate scan failed", "error", scanErr)
			return
		}
		candidates = append(candidates, item)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		rows.Close()
		s.Log.Warn("scheduler candidate read failed", "error", rowsErr)
		return
	}
	rows.Close()
	batchIDs := make(map[uuid.UUID]uuid.UUID)
	for _, item := range candidates {
		args := GenerateArgs{GenerationJobID: item.id.String(), ExecutionGeneration: riverExecutionGeneration(item.executionGeneration)}
		if item.providerJobID != nil {
			// Keep the remote ID in the unique arguments for compatibility with
			// jobs queued before execution_generation was introduced.
			args.ReconciledProviderJobID = *item.providerJobID
		}
		result, insertErr := s.River.InsertTx(ctx, tx, args, generationRiverInsertOpts())
		if insertErr != nil {
			s.Log.Warn("river insert failed", "error", insertErr, "generation_job_id", item.id)
			return
		}
		if result == nil || result.Job == nil {
			s.Log.Warn("river insert returned no job", "generation_job_id", item.id)
			return
		}
		if result.UniqueSkippedAsDuplicate {
			// With execution_generation in the unique args, a duplicate belongs to
			// this exact business execution and is safe to reattach.
			s.Log.Debug("reusing River job for current execution", "generation_job_id", item.id, "execution_generation", item.executionGeneration, "river_job_id", result.Job.ID)
		}
		var batchID, ownerID uuid.UUID
		var status string
		err = tx.QueryRow(ctx, `UPDATE generation_jobs SET dispatch_state='dispatched',
			status=CASE WHEN provider_job_id IS NULL THEN 'dispatched' ELSE 'provider_pending' END,
			upstream_active_until=NULL,river_job_id=$2,dispatched_at=now(),updated_at=now()
			WHERE id=$1 AND dispatch_state='pending' RETURNING batch_id,owner_user_id,status`, item.id, result.Job.ID).Scan(&batchID, &ownerID, &status)
		if err != nil {
			s.Log.Warn("scheduler job state transition failed", "error", err, "generation_job_id", item.id)
			return
		}
		batchIDs[batchID] = ownerID
		if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload) VALUES($1,$2,$3,'job.dispatched',jsonb_build_object('status',$4::text))`, ownerID, batchID, item.id, status); err != nil {
			s.Log.Warn("scheduler job event insert failed", "error", err, "generation_job_id", item.id)
			return
		}
	}
	for batchID, ownerID := range batchIDs {
		result, updateErr := tx.Exec(ctx, `UPDATE generation_batches SET status='running',updated_at=now() WHERE id=$1 AND status='queued'`, batchID)
		if updateErr != nil {
			s.Log.Warn("scheduler batch transition failed", "error", updateErr, "batch_id", batchID)
			return
		}
		if result.RowsAffected() > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,event_type,payload) VALUES($1,$2,'batch.updated',jsonb_build_object('status','running'))`, ownerID, batchID); err != nil {
				s.Log.Warn("scheduler batch event insert failed", "error", err, "batch_id", batchID)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		s.Log.Warn("scheduler commit failed", "error", err)
	}
}

func generationRiverInsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		MaxAttempts: 8,
		Queue:       "generation",
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByQueue: true,
			ByState: []rivertype.JobState{
				rivertype.JobStateAvailable,
				rivertype.JobStatePending,
				rivertype.JobStateRetryable,
				rivertype.JobStateRunning,
				rivertype.JobStateScheduled,
			},
		},
	}
}

func (s *Scheduler) recoverStagedIngests(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT j.id,j.batch_id,j.owner_user_id,j.river_job_id,j.execution_generation
		FROM generation_jobs j
		WHERE j.status='ingesting' AND j.updated_at<now()-interval '30 seconds'
		  AND EXISTS(SELECT 1 FROM generation_staged_outputs o WHERE o.job_id=j.id)
		ORDER BY j.updated_at,j.id FOR UPDATE SKIP LOCKED LIMIT 16`)
	if err != nil {
		return err
	}
	type recovery struct {
		jobID               uuid.UUID
		batchID             uuid.UUID
		ownerID             uuid.UUID
		riverJobID          *int64
		executionGeneration int
	}
	items := make([]recovery, 0, 16)
	for rows.Next() {
		var item recovery
		if err = rows.Scan(&item.jobID, &item.batchID, &item.ownerID, &item.riverJobID, &item.executionGeneration); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range items {
		result, insertErr := s.River.InsertTx(ctx, tx, GenerateArgs{GenerationJobID: item.jobID.String(), ExecutionGeneration: riverExecutionGeneration(item.executionGeneration)}, generationRiverInsertOpts())
		if insertErr != nil {
			return insertErr
		}
		if result == nil || result.Job == nil {
			return errors.New("staged recovery River insert returned no job")
		}
		changed := item.riverJobID == nil || *item.riverJobID != result.Job.ID
		if _, err = tx.Exec(ctx, `UPDATE generation_jobs SET river_job_id=$2,updated_at=now() WHERE id=$1 AND status='ingesting'`, item.jobID, result.Job.ID); err != nil {
			return err
		}
		if changed {
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
				VALUES($1,$2,$3,'job.recovery_scheduled',jsonb_build_object('status','ingesting'))`, item.ownerID, item.batchID, item.jobID); err != nil {
				return err
			}
		}
	}
	return nil
}
