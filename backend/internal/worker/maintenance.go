package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/blob"
)

type Maintenance struct {
	DB        *pgxpool.Pool
	Blobs     *blob.Local
	AssetRoot string
	Log       *slog.Logger
	Generator *GenerateWorker
}

func (m *Maintenance) Run(ctx context.Context) {
	m.cleanup(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.cleanup(ctx)
		}
	}
}

func (m *Maintenance) cleanup(ctx context.Context) {
	for _, directory := range []string{filepath.Join(m.AssetRoot, "uploads", "tmp"), filepath.Join(m.AssetRoot, "uploads", "quarantine")} {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			info, err := entry.Info()
			if err == nil && !entry.IsDir() && time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(filepath.Join(directory, entry.Name()))
			}
		}
	}
	rows, err := m.DB.Query(ctx, `WITH candidates AS (
		SELECT a.id FROM assets a
		WHERE a.expires_at<=now() AND a.purged_at IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM generation_input_assets input
			JOIN generation_jobs job ON job.batch_id=input.batch_id
			WHERE input.asset_id=a.id
			  AND (job.status NOT IN ('succeeded','failed','cancelled')
			       OR (job.status='failed' AND job.retryable=true))
		  )
		ORDER BY a.expires_at,a.id LIMIT 500 FOR UPDATE SKIP LOCKED
	)
	UPDATE assets a SET purge_pending=true FROM candidates c WHERE a.id=c.id
	RETURNING a.id,a.storage_key,a.sha256`)
	if err != nil {
		m.Log.Warn("asset expiry scan failed", "error", err)
		return
	}
	type expired struct {
		id     uuid.UUID
		key    string
		digest string
	}
	items := make([]expired, 0)
	for rows.Next() {
		var item expired
		if err := rows.Scan(&item.id, &item.key, &item.digest); err != nil {
			rows.Close()
			m.Log.Warn("asset expiry result scan failed", "error", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		m.Log.Warn("asset expiry result read failed", "error", err)
		return
	}
	rows.Close()
	for _, item := range items {
		if m.Blobs == nil {
			m.Log.Error("asset expiry skipped because content lease manager is unavailable", "asset_id", item.id)
			continue
		}
		lease := m.Blobs.AcquireContentLease()
		purgeErr := m.purgeExpiredAsset(ctx, item.id, item.key, item.digest)
		lease.Release()
		if purgeErr != nil {
			m.Log.Warn("expired asset purge failed", "asset_id", item.id, "error", purgeErr)
		}
	}
	retentionStatements := []struct {
		name string
		sql  string
	}{
		{"job events", `DELETE FROM job_events WHERE created_at<now()-interval '90 days'`},
		{"callback events", `DELETE FROM provider_callback_events WHERE created_at<now()-interval '30 days'`},
		{"provider attempts", `DELETE FROM provider_attempts WHERE created_at<now()-interval '90 days'`},
		{"generation batches", `DELETE FROM generation_batches WHERE created_at<now()-interval '90 days'`},
		{"user sessions", `DELETE FROM user_sessions WHERE expires_at<now() OR (revoked_at IS NOT NULL AND revoked_at<now()-interval '7 days')`},
		{"expire upload sessions", `UPDATE upload_sessions SET status='expired',updated_at=now() WHERE expires_at<now() AND status IN ('created','uploading','validating')`},
		{"delete upload sessions", `DELETE FROM upload_sessions WHERE status IN ('ready','failed','expired') AND expires_at<now()-interval '90 days'`},
	}
	for _, statement := range retentionStatements {
		if _, err := m.DB.Exec(ctx, statement.sql); err != nil {
			m.Log.Warn("retention maintenance failed", "dataset", statement.name, "error", err)
		}
	}
	storageStats, storageErr := m.maintainContentStorage(ctx)
	if storageErr != nil {
		m.Log.Warn("content storage maintenance skipped", "error", storageErr)
	}
	m.Log.Info("maintenance completed",
		"expired_assets", len(items),
		"orphan_dirs_scanned", storageStats.scanned,
		"orphan_dirs_skipped", storageStats.skipped,
		"orphan_dirs_deleted", storageStats.deleted,
		"orphan_bytes_deleted", storageStats.bytesDeleted,
		"thumbnail_assets_scanned", storageStats.thumbnailScanned,
		"thumbnail_repair_attempts", storageStats.thumbnailRepairs,
		"thumbnail_unsafe", storageStats.thumbnailUnsafe,
	)
}

func (m *Maintenance) purgeExpiredAsset(ctx context.Context, assetID uuid.UUID, storageKey, digest string) error {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// A retryable/uncertain draw still needs its original references. Creation
	// and manual retry take a conflicting row lock and reject purge_pending, so
	// this second check closes the mark/delete race.
	var inputInUse bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM generation_input_assets input
		JOIN generation_jobs job ON job.batch_id=input.batch_id
		WHERE input.asset_id=$1
		  AND (job.status NOT IN ('succeeded','failed','cancelled')
		       OR (job.status='failed' AND job.retryable=true))
	)`, assetID).Scan(&inputInUse); err != nil {
		return err
	}
	if inputInUse {
		_, err = tx.Exec(ctx, `UPDATE assets SET purge_pending=false WHERE id=$1 AND purged_at IS NULL`, assetID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	var contentReferenced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM assets WHERE sha256=$1 AND id<>$2 AND purged_at IS NULL
		UNION ALL SELECT 1 FROM generation_staged_outputs WHERE sha256=$1
	)`, digest, assetID).Scan(&contentReferenced); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE assets SET purged_at=now(),purge_pending=false WHERE id=$1 AND purge_pending=true AND purged_at IS NULL`, assetID)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("asset purge mark lost its lease")
	}
	// Commit the authoritative tombstone before removing bytes. A failed file
	// deletion leaves a harmless orphan for storage maintenance; deleting first
	// could leave a live database row pointing at permanently missing content.
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if !contentReferenced {
		if _, err = deleteCanonicalContent(m.AssetRoot, storageKey, digest, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

type contentStorageMaintenanceStats struct {
	scanned          int
	skipped          int
	deleted          int
	bytesDeleted     int64
	thumbnailScanned int
	thumbnailRepairs int
	thumbnailUnsafe  int
}

func (m *Maintenance) maintainContentStorage(ctx context.Context) (contentStorageMaintenanceStats, error) {
	stats := contentStorageMaintenanceStats{}
	references, err := m.loadStorageReferences(ctx)
	if err != nil {
		return stats, err
	}
	candidates, scanStats, err := scanOrphanCandidates(ctx, m.AssetRoot, references, time.Now(), defaultOrphanMinAge, defaultOrphanScanLimit, defaultOrphanDeleteLimit)
	stats.scanned, stats.skipped = scanStats.Scanned, scanStats.Skipped
	if err != nil {
		return stats, err
	}
	// Rebuild the mark set after filesystem scanning. A result staged while the
	// scan was running must win over deletion.
	references, err = m.loadStorageReferences(ctx)
	if err != nil {
		return stats, err
	}
	for _, candidate := range candidates {
		if _, referenced := references[candidate.Digest]; referenced {
			stats.skipped++
			continue
		}
		if m.Blobs == nil {
			return stats, errors.New("content lease manager is unavailable")
		}
		lease := m.Blobs.AcquireContentLease()
		referenced, referenceErr := m.storageDigestReferenced(ctx, candidate.Digest)
		if referenceErr != nil || referenced {
			lease.Release()
			stats.skipped++
			if referenceErr != nil {
				m.Log.Warn("orphan asset reference recheck failed", "sha256", candidate.Digest, "error", referenceErr)
			}
			continue
		}
		deletedBytes, deleteErr := deleteOrphanCandidate(candidate, time.Now(), defaultOrphanMinAge)
		lease.Release()
		if deleteErr != nil {
			stats.skipped++
			m.Log.Warn("orphan asset deletion skipped", "sha256", candidate.Digest, "error", deleteErr)
			continue
		}
		stats.deleted++
		stats.bytesDeleted += deletedBytes
	}
	if m.Generator == nil || ctx.Err() != nil {
		return stats, ctx.Err()
	}
	keys, scanned, unsafe := findMissingThumbnailKeys(m.AssetRoot, references, defaultThumbnailScanLimit, defaultThumbnailRepairLimit)
	stats.thumbnailScanned, stats.thumbnailUnsafe = scanned, unsafe
	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		m.Generator.makeThumbnails(ctx, key)
		stats.thumbnailRepairs++
	}
	return stats, ctx.Err()
}

func (m *Maintenance) storageDigestReferenced(ctx context.Context, digest string) (bool, error) {
	var referenced bool
	err := m.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM assets WHERE sha256=$1 AND purged_at IS NULL
		UNION ALL SELECT 1 FROM generation_staged_outputs WHERE sha256=$1
	)`, digest).Scan(&referenced)
	return referenced, err
}

func (m *Maintenance) loadStorageReferences(ctx context.Context) (map[string]storageReference, error) {
	rows, err := m.DB.Query(ctx, `SELECT storage_key,sha256,true FROM assets WHERE purged_at IS NULL
		UNION ALL SELECT storage_key,sha256,false FROM generation_staged_outputs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	references := make(map[string]storageReference)
	for rows.Next() {
		var storageKey, digest string
		var activeAsset bool
		if err := rows.Scan(&storageKey, &digest, &activeAsset); err != nil {
			return nil, err
		}
		if err := addStorageReference(references, storageKey, digest, activeAsset); err != nil {
			return nil, fmt.Errorf("invalid database storage reference: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return references, nil
}
