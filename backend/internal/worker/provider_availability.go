package worker

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const unavailableProviderJobBatchSize = 100

func failUnavailableProviderJobs(ctx context.Context, db *pgxpool.Pool, providerID string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "provider-slot:"+providerID); err != nil {
		return err
	}
	var enabled bool
	var state string
	if err = tx.QueryRow(ctx, `SELECT enabled,state FROM providers WHERE id=$1 FOR UPDATE`, providerID).Scan(&enabled, &state); err != nil {
		return err
	}
	if enabled && state != "paused" {
		return tx.Commit(ctx)
	}
	if err = failUnavailableProviderJobsInTx(ctx, tx, providerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func failUnavailableProviderJobsInTx(ctx context.Context, tx pgx.Tx, providerID string) error {
	batchIDs := make(map[uuid.UUID]struct{})
	for {
		type failedJob struct {
			id, batchID, ownerID uuid.UUID
		}
		rows, err := tx.Query(ctx, `WITH candidates AS (
			SELECT j.id
			FROM generation_jobs j
			JOIN generation_batches b ON b.id=j.batch_id
			JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
			WHERE v.config->>'provider'=$1 AND j.provider_job_id IS NULL
			  AND j.status IN ('queued','dispatched')
			ORDER BY j.created_at,j.id
			LIMIT $2
			FOR UPDATE OF j SKIP LOCKED
		)
		UPDATE generation_jobs j SET status='failed',dispatch_state='finished',
			error_code='PROVIDER_UNAVAILABLE',error_message=$3,retryable=true,
			upstream_active_until=NULL,completed_at=now(),updated_at=now()
		FROM candidates WHERE j.id=candidates.id
		RETURNING j.id,j.batch_id,j.owner_user_id`, providerID, unavailableProviderJobBatchSize, userFacingGenerationError("PROVIDER_UNAVAILABLE"))
		if err != nil {
			return err
		}
		failed := make([]failedJob, 0, unavailableProviderJobBatchSize)
		for rows.Next() {
			var job failedJob
			if err = rows.Scan(&job.id, &job.batchID, &job.ownerID); err != nil {
				rows.Close()
				return err
			}
			failed = append(failed, job)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, job := range failed {
			batchIDs[job.batchID] = struct{}{}
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,batch_id,job_id,event_type,payload)
				VALUES($1,$2,$3,'job.failed',jsonb_build_object('status','failed','error_code','PROVIDER_UNAVAILABLE','message',$4::text,'retryable',true))`,
				job.ownerID, job.batchID, job.id, userFacingGenerationError("PROVIDER_UNAVAILABLE")); err != nil {
				return err
			}
		}
		if len(failed) < unavailableProviderJobBatchSize {
			break
		}
	}
	for batchID := range batchIDs {
		if _, err := reconcileWorkerBatch(ctx, tx, batchID); err != nil {
			return err
		}
	}
	for {
		type failedOperation struct {
			id, projectID, ownerID uuid.UUID
		}
		rows, err := tx.Query(ctx, `WITH candidates AS (
			SELECT id FROM asset_operations
			WHERE provider_id=$1 AND status IN ('queued','dispatched')
			ORDER BY created_at,id LIMIT $2 FOR UPDATE SKIP LOCKED
		)
		UPDATE asset_operations o SET status='failed',dispatch_state='finished',error_code='PROVIDER_UNAVAILABLE',
			error_message='智能分层服务暂不可用，请稍后重新发起',completed_at=now(),updated_at=now()
		FROM candidates WHERE o.id=candidates.id RETURNING o.id,o.editor_project_id,o.owner_user_id`, providerID, unavailableProviderJobBatchSize)
		if err != nil {
			return err
		}
		failed := make([]failedOperation, 0, unavailableProviderJobBatchSize)
		for rows.Next() {
			var operation failedOperation
			if err = rows.Scan(&operation.id, &operation.projectID, &operation.ownerID); err != nil {
				rows.Close()
				return err
			}
			failed = append(failed, operation)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, operation := range failed {
			if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload)
				VALUES($1,$2,$3,'asset_operation.failed',jsonb_build_object('id',$2::uuid,'status','failed','editor_project_id',$3::uuid,
				'error_code','PROVIDER_UNAVAILABLE','error_message','智能分层服务暂不可用，请稍后重新发起'))`,
				operation.ownerID, operation.id, operation.projectID); err != nil {
				return err
			}
		}
		if len(failed) < unavailableProviderJobBatchSize {
			break
		}
	}
	return nil
}
