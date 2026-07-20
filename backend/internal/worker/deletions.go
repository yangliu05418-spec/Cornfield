package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/blob"
)

type DeletionProcessor struct {
	DB        *pgxpool.Pool
	Blobs     *blob.Local
	AssetRoot string
	Log       *slog.Logger
	Wake      chan struct{}
}

type deletionRequest struct {
	ID           uuid.UUID
	Kind         string
	AssetID      *uuid.UUID
	TargetUserID *uuid.UUID
}

func (p *DeletionProcessor) Run(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		p.processOne(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.Wake:
		}
	}
}

func (p *DeletionProcessor) ListenNotifications(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := p.DB.Acquire(ctx)
		if err != nil {
			p.wait(ctx)
			continue
		}
		_, err = conn.Exec(ctx, "LISTEN deletion_requests")
		if err == nil {
			for ctx.Err() == nil {
				if _, err = conn.Conn().WaitForNotification(ctx); err != nil {
					break
				}
				select {
				case p.Wake <- struct{}{}:
				default:
				}
			}
		}
		conn.Release()
		p.wait(ctx)
	}
}

func (p *DeletionProcessor) wait(ctx context.Context) {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (p *DeletionProcessor) processOne(ctx context.Context) {
	request, ok, err := p.claim(ctx)
	if err != nil {
		p.Log.Warn("deletion claim failed", "error", err)
		return
	}
	if !ok {
		return
	}
	if request.Kind == "asset" {
		err = p.deleteAsset(ctx, *request.AssetID)
	} else {
		err = p.deleteUser(ctx, *request.TargetUserID)
	}
	if errors.Is(err, errAssetInUse) || errors.Is(err, errUserDeletionWaiting) || errors.Is(err, errOrphanCandidateChanged) {
		_, _ = p.DB.Exec(ctx, `UPDATE deletion_requests SET status='pending',started_at=NULL,next_attempt_at=now()+interval '2 seconds'
			WHERE id=$1 AND status='running'`, request.ID)
		return
	}
	if err != nil {
		p.Log.Warn("deletion failed", "deletion_id", request.ID, "error", err)
		_, _ = p.DB.Exec(ctx, `UPDATE deletion_requests SET
			status=CASE WHEN attempt_count>=8 THEN 'failed' ELSE 'pending' END,
			error_code='DELETE_FAILED',error_message=$2,
			completed_at=CASE WHEN attempt_count>=8 THEN now() ELSE NULL END,
			started_at=NULL,
			next_attempt_at=now()+LEAST(interval '5 minutes',interval '2 seconds'*power(2,GREATEST(attempt_count-1,0)))
			WHERE id=$1`, request.ID, boundedDeletionError(err.Error()))
		return
	}
	_, _ = p.DB.Exec(ctx, `UPDATE deletion_requests SET status='succeeded',completed_at=now(),error_code=NULL,error_message=NULL WHERE id=$1`, request.ID)
}

func (p *DeletionProcessor) claim(ctx context.Context) (deletionRequest, bool, error) {
	var item deletionRequest
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT id,kind,asset_id,target_user_id FROM deletion_requests
		WHERE (status='pending' AND next_attempt_at<=now()) OR (status='running' AND started_at<now()-interval '10 minutes')
		ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&item.ID, &item.Kind, &item.AssetID, &item.TargetUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE deletion_requests SET status='running',started_at=now(),attempt_count=attempt_count+1 WHERE id=$1`, item.ID); err != nil {
		return item, false, err
	}
	return item, true, tx.Commit(ctx)
}

func (p *DeletionProcessor) deleteAsset(ctx context.Context, assetID uuid.UUID) error {
	var key, digest string
	var purged *time.Time
	if err := p.DB.QueryRow(ctx, `SELECT storage_key,sha256,purged_at FROM assets WHERE id=$1`, assetID).Scan(&key, &digest, &purged); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	lease := p.Blobs.AcquireContentLease()
	defer lease.Release()
	maintenance := &Maintenance{DB: p.DB, Blobs: p.Blobs, AssetRoot: p.AssetRoot, Log: p.Log}
	if purged == nil {
		return maintenance.purgeAsset(ctx, assetID, key, digest, false)
	}
	referenced, err := maintenance.storageDigestReferenced(ctx, digest)
	if err != nil || referenced {
		return err
	}
	_, err = deleteCanonicalContent(p.AssetRoot, key, digest, time.Now())
	return err
}

var errUserDeletionWaiting = errors.New("user deletion is waiting for active generations")

func (p *DeletionProcessor) deleteUser(ctx context.Context, userID uuid.UUID) error {
	var active int
	if err := p.DB.QueryRow(ctx, `SELECT count(*) FROM generation_jobs WHERE owner_user_id=$1
		AND (status NOT IN ('succeeded','failed','cancelled') OR upstream_active_until>now())`, userID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return errUserDeletionWaiting
	}
	rows, err := p.DB.Query(ctx, `UPDATE assets SET purge_pending=true WHERE owner_user_id=$1 AND purged_at IS NULL
		RETURNING id`, userID)
	if err != nil {
		return err
	}
	assetIDs := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		assetIDs = append(assetIDs, id)
	}
	rows.Close()
	for _, assetID := range assetIDs {
		if err = p.deleteAsset(ctx, assetID); err != nil {
			return err
		}
	}
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, statement := range []string{
		`DELETE FROM upload_sessions WHERE owner_user_id=$1`,
		`DELETE FROM generation_batches WHERE owner_user_id=$1`,
		`DELETE FROM asset_folders WHERE owner_user_id=$1`,
		`DELETE FROM generation_rate_limits WHERE owner_user_id=$1`,
		`DELETE FROM job_events WHERE owner_user_id=$1`,
		`DELETE FROM user_sessions WHERE user_id=$1`,
		`DELETE FROM deletion_requests WHERE asset_id IN (SELECT id FROM assets WHERE owner_user_id=$1)`,
		`DELETE FROM assets WHERE owner_user_id=$1`,
		`UPDATE audit_logs SET metadata='{}'::jsonb WHERE target_type='user' AND target_id=$1::text`,
	} {
		if _, err = tx.Exec(ctx, statement, userID); err != nil {
			return err
		}
	}
	deletedUsername := "deleted-" + userID.String()
	_, err = tx.Exec(ctx, `UPDATE users SET username=$2,display_name='已删除用户',password_hash='deleted',status='deleted',
		must_change_password=false,temporary_password_expires_at=NULL,updated_at=now() WHERE id=$1 AND status='deleting'`, userID, deletedUsername)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func boundedDeletionError(message string) string {
	const limit = 500
	if len(message) > limit {
		return message[:limit]
	}
	return message
}
