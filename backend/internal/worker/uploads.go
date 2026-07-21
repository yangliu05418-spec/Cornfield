package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/blob"
)

type UploadValidator struct {
	DB        *pgxpool.Pool
	Blobs     *blob.Local
	AssetRoot string
	Generator *GenerateWorker
	Log       *slog.Logger
}

func (v *UploadValidator) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for v.processOne(ctx) {
			}
		}
	}
}

func (v *UploadValidator) processOne(ctx context.Context) bool {
	var id, ownerID uuid.UUID
	var filename, declaredMedia, quarantineKey string
	err := v.DB.QueryRow(ctx, `SELECT s.id,s.owner_user_id,s.original_filename,s.declared_media_type,s.quarantine_key
		FROM upload_sessions s JOIN users u ON u.id=s.owner_user_id
		WHERE s.status='validating' AND s.expires_at>now() AND u.status='active'
		ORDER BY s.created_at LIMIT 1`).Scan(&id, &ownerID, &filename, &declaredMedia, &quarantineKey)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			v.Log.Warn("upload validation queue read failed", "error", err)
		}
		return false
	}
	path := filepath.Join(v.AssetRoot, "uploads", "quarantine", filepath.Base(quarantineKey))
	file, err := os.Open(path)
	if err != nil {
		v.fail(ctx, id, "QUARANTINE_MISSING")
		return true
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 25*1024*1024+1))
	file.Close()
	if readErr != nil || len(data) > 25*1024*1024 {
		v.fail(ctx, id, "IMAGE_READ_FAILED")
		return true
	}
	media, extension, width, height, validateErr := validateImage(data)
	if validateErr != nil || media != declaredMedia {
		v.fail(ctx, id, "IMAGE_INVALID")
		_ = os.Remove(path)
		return true
	}
	// Reject oversized dimensions from headers before asking libvips to fully
	// decode the file. This bounds decompression-bomb exposure while the
	// libvips pass still catches truncated/corrupt pixel data.
	validationOutput := filepath.Join(v.AssetRoot, "uploads", "tmp", id.String()+"-validation.webp")
	decodeCtx, cancelDecode := context.WithTimeout(ctx, 45*time.Second)
	command := exec.CommandContext(decodeCtx, "vipsthumbnail", path, "--size", "1x", "--output", validationOutput+"[strip]")
	command.Env = append(os.Environ(), "VIPS_CONCURRENCY=2", "VIPS_DISC_THRESHOLD=268435456", "MALLOC_ARENA_MAX=2")
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		cancelDecode()
		if ctx.Err() != nil {
			_ = os.Remove(validationOutput)
			return false
		}
		v.fail(ctx, id, "IMAGE_DECODE_FAILED")
		v.Log.Warn("upload rejected by libvips", "upload_id", id, "error", commandErr, "detail", string(output))
		_ = os.Remove(path)
		_ = os.Remove(validationOutput)
		return true
	}
	cancelDecode()
	_ = os.Remove(validationOutput)
	tx, err := v.DB.Begin(ctx)
	if err != nil {
		return false
	}
	defer tx.Rollback(ctx)
	var ownerStatus string
	if err = tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1 FOR UPDATE`, ownerID).Scan(&ownerStatus); err != nil {
		return false
	}
	if ownerStatus != "active" {
		_ = tx.Rollback(ctx)
		v.fail(ctx, id, "OWNER_UNAVAILABLE")
		_ = os.Remove(path)
		return true
	}
	contentLease := v.Blobs.AcquireContentLease()
	leaseReleased := false
	defer func() {
		if !leaseReleased {
			contentLease.Release()
		}
	}()
	key, digest, size, err := contentLease.PutImmutable(path, extension)
	if err != nil {
		v.Log.Warn("upload immutable commit failed", "upload_id", id, "error", err)
		return false
	}
	blurDataURL, err := v.Generator.ensurePresentationVariants(ctx, key)
	if err != nil {
		v.Log.Warn("upload presentation variants failed", "upload_id", id, "error", err)
		_ = tx.Rollback(ctx)
		maintenance := &Maintenance{DB: v.DB, Blobs: v.Blobs, AssetRoot: v.AssetRoot, Log: v.Log}
		referenced, referenceErr := maintenance.storageDigestReferenced(ctx, digest)
		if referenceErr == nil && !referenced {
			_, _ = deleteCanonicalContent(v.AssetRoot, key, digest, time.Now())
		}
		v.fail(ctx, id, "UPLOAD_VARIANT_FAILED")
		contentLease.Release()
		leaseReleased = true
		return true
	}
	var assetID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,original_filename,width,height,byte_size,blur_data_url)
		VALUES($1,'upload',$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, ownerID, key, digest, media, filename, width, height, size, blurDataURL).Scan(&assetID)
	if err == nil {
		command, updateErr := tx.Exec(ctx, `UPDATE upload_sessions SET status='ready',asset_id=$2,updated_at=now()
			WHERE id=$1 AND status='validating' AND expires_at>now()`, id, assetID)
		err = updateErr
		if err == nil && command.RowsAffected() != 1 {
			err = errors.New("upload validation lease expired before commit")
		}
	}
	if err != nil {
		v.Log.Warn("upload database update failed", "upload_id", id, "error", err)
		_ = tx.Rollback(ctx)
		maintenance := &Maintenance{DB: v.DB, Blobs: v.Blobs, AssetRoot: v.AssetRoot, Log: v.Log}
		referenced, referenceErr := maintenance.storageDigestReferenced(ctx, digest)
		if referenceErr != nil {
			v.Log.Warn("upload rollback reference check failed", "upload_id", id, "error", referenceErr)
			return false
		}
		if !referenced {
			if _, deleteErr := deleteCanonicalContent(v.AssetRoot, key, digest, time.Now()); deleteErr != nil {
				v.Log.Warn("upload rollback content cleanup failed", "upload_id", id, "error", deleteErr)
				return false
			}
		}
		v.fail(ctx, id, "UPLOAD_COMMIT_FAILED")
		contentLease.Release()
		leaseReleased = true
		return true
	}
	if err = tx.Commit(ctx); err != nil {
		v.Log.Warn("upload database commit failed", "upload_id", id, "error", err)
		return false
	}
	contentLease.Release()
	leaseReleased = true
	v.Generator.queueOptionalThumbnail(key)
	return true
}

func (v *UploadValidator) fail(ctx context.Context, id uuid.UUID, code string) {
	if _, err := v.DB.Exec(ctx, `UPDATE upload_sessions SET status='failed',error_code=$2,updated_at=now()
		WHERE id=$1 AND status='validating' AND expires_at>now()`, id, code); err != nil {
		v.Log.Warn("upload validation failure state update failed", "upload_id", id, "error", err)
	}
}
