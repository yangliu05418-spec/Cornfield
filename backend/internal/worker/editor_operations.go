package worker

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	stdDraw "image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
	"golang.org/x/sync/errgroup"

	"internal-image-studio/internal/blob"
	"internal-image-studio/internal/config"
	studioEditor "internal-image-studio/internal/editor"
	"internal-image-studio/internal/modelconfig"
	"internal-image-studio/internal/provider"
)

const (
	maxLayerInputBytes = int64(30 << 20)
	maxLayerGroupBytes = int64(512 << 20)
)

type AssetOperationWorker struct {
	river.WorkerDefaults[AssetOperationArgs]
	DB          *pgxpool.Pool
	Config      config.Config
	Catalog     *modelconfig.Catalog
	Blobs       *blob.Local
	Decomposers map[string]provider.LayerDecomposer
	ProviderSem map[string]chan struct{}
	RenderSem   chan struct{}
	HTTPClient  *http.Client
	Generator   *GenerateWorker
	Log         *slog.Logger
}

type assetOperationRecord struct {
	ID                     uuid.UUID
	OwnerID                uuid.UUID
	ProjectID              uuid.UUID
	Type                   string
	Status                 string
	SourceRevision         int64
	SourceDocument         []byte
	SnapshotAssetID        *uuid.UUID
	LayerSetID             *uuid.UUID
	ModelID                *string
	CapabilityRevision     *string
	Prompt                 *string
	Resolution             *string
	PromptOptimizationMode *string
	ProviderID             *string
	ProviderModel          *string
	ProviderRequestID      *string
	StagedManifest         []byte
	RetryCount             int
}

type stagedLayerManifest struct {
	Items []stagedLayerItem `json:"items"`
	Usage map[string]any    `json:"usage,omitempty"`
}

type stagedLayerItem struct {
	URL         string                     `json:"url,omitempty"`
	StorageKey  string                     `json:"storage_key,omitempty"`
	SHA256      string                     `json:"sha256,omitempty"`
	ByteSize    int64                      `json:"byte_size,omitempty"`
	MediaType   string                     `json:"media_type"`
	Width       int                        `json:"width,omitempty"`
	Height      int                        `json:"height,omitempty"`
	BlurDataURL string                     `json:"blur_data_url,omitempty"`
	ZIndex      int                        `json:"z_index"`
	BoundingBox *provider.LayerBoundingBox `json:"bounding_box,omitempty"`
	Name        string                     `json:"name,omitempty"`
	Description string                     `json:"description,omitempty"`
}

func (w *AssetOperationWorker) Middleware(_ *rivertype.JobRow) []rivertype.WorkerMiddleware {
	return []rivertype.WorkerMiddleware{river.WorkerMiddlewareFunc(func(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
		err := doInner(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var args AssetOperationArgs
		if json.Unmarshal(job.EncodedArgs, &args) == nil {
			if id, parseErr := uuid.Parse(args.AssetOperationID); parseErr == nil {
				uncertain, persistErr := w.hasUnfinishedSubmitAttempt(id)
				if persistErr == nil && uncertain {
					_ = w.persistOperationUncertain(id, "SUBMISSION_UNCERTAIN", "智能分层提交结果无法确认，请联系管理员核查")
					return river.JobCancel(err)
				}
			}
		}
		// Snapshot, ingest, publish and package work is idempotent. Let River
		// retry those stages instead of incorrectly treating them as a paid
		// provider submission with an unknown outcome.
		return err
	})}
}

func (w *AssetOperationWorker) Work(ctx context.Context, riverJob *river.Job[AssetOperationArgs]) error {
	id, err := uuid.Parse(riverJob.Args.AssetOperationID)
	if err != nil {
		return river.JobCancel(err)
	}
	record, err := w.loadAssetOperation(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if operationTerminal(record.Status) {
		return nil
	}
	switch record.Type {
	case "editor_publish":
		return w.publishEditorDocument(ctx, record)
	case "layer_package":
		return w.packageLayerSet(ctx, record)
	case "layer_decomposition":
		return w.decompose(ctx, record)
	default:
		return w.failOperation(ctx, record, "ASSET_OPERATION_UNSUPPORTED", "图片处理类型不受支持")
	}
}

func operationTerminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled" || status == "submission_uncertain"
}

func (w *AssetOperationWorker) loadAssetOperation(ctx context.Context, id uuid.UUID) (assetOperationRecord, error) {
	var record assetOperationRecord
	err := w.DB.QueryRow(ctx, `SELECT id,owner_user_id,editor_project_id,operation_type,status,source_revision,source_document,
		snapshot_asset_id,layer_set_id,model_id,capability_revision,prompt,resolution,prompt_optimization_mode,provider_id,provider_model,
		provider_request_id,COALESCE(staged_manifest,'null'::jsonb),retry_count
		FROM asset_operations WHERE id=$1`, id).Scan(
		&record.ID, &record.OwnerID, &record.ProjectID, &record.Type, &record.Status, &record.SourceRevision,
		&record.SourceDocument, &record.SnapshotAssetID, &record.LayerSetID, &record.ModelID, &record.CapabilityRevision, &record.Prompt,
		&record.Resolution, &record.PromptOptimizationMode, &record.ProviderID, &record.ProviderModel,
		&record.ProviderRequestID, &record.StagedManifest, &record.RetryCount,
	)
	return record, err
}

func (w *AssetOperationWorker) decompose(ctx context.Context, record assetOperationRecord) error {
	if record.SnapshotAssetID == nil {
		if err := w.setOperationStatus(ctx, record, "snapshotting", "正在准备当前画布"); err != nil {
			return err
		}
		assetID, err := w.renderAndStoreSnapshot(ctx, record)
		if err != nil {
			return err
		}
		record.SnapshotAssetID = &assetID
	}
	manifest := stagedLayerManifest{}
	if string(record.StagedManifest) != "null" && len(record.StagedManifest) > 0 {
		if err := json.Unmarshal(record.StagedManifest, &manifest); err != nil {
			return w.failOperation(ctx, record, "LAYER_MANIFEST_INVALID", "智能分层结果记录损坏，请重新发起分层")
		}
	}
	if len(manifest.Items) == 0 {
		if record.Status == "submitting" {
			unfinished, checkErr := w.hasUnfinishedSubmitAttempt(record.ID)
			if checkErr != nil {
				return checkErr
			}
			if unfinished {
				if persistErr := w.persistOperationUncertain(record.ID, "SUBMISSION_UNCERTAIN", "智能分层提交结果无法确认，请联系管理员核查"); persistErr != nil {
					return persistErr
				}
				return river.JobCancel(errors.New("SUBMISSION_UNCERTAIN"))
			}
		}
		if record.ProviderID == nil || record.ProviderModel == nil {
			return w.failOperation(ctx, record, "PROVIDER_NOT_CONFIGURED", "智能分层服务未配置")
		}
		decomposer := w.Decomposers[*record.ProviderID]
		if decomposer == nil {
			return w.failOperation(ctx, record, "PROVIDER_NOT_CONFIGURED", "智能分层服务未配置")
		}
		breakerKey := *record.ProviderID + ":" + valueOrPointer(record.ModelID)
		if w.Generator != nil && w.Generator.Breaker != nil && !w.Generator.Breaker.Allow(breakerKey) {
			return w.deferOperationForBreaker(ctx, record)
		}
		dataURL, err := w.snapshotDataURL(ctx, *record.SnapshotAssetID)
		if err != nil {
			if w.Generator != nil && w.Generator.Breaker != nil {
				w.Generator.Breaker.Abandon(breakerKey)
			}
			return err
		}
		if err = w.setOperationStatus(ctx, record, "submitting", "正在识别画面结构"); err != nil {
			if w.Generator != nil && w.Generator.Breaker != nil {
				w.Generator.Breaker.Abandon(breakerKey)
			}
			return err
		}
		release, err := w.acquireOperationProvider(ctx, *record.ProviderID)
		if err != nil {
			if w.Generator != nil && w.Generator.Breaker != nil {
				w.Generator.Breaker.Abandon(breakerKey)
			}
			return err
		}
		attemptID, err := w.beginAssetAttempt(ctx, record, "submit")
		if err != nil {
			release()
			if w.Generator != nil && w.Generator.Breaker != nil {
				w.Generator.Breaker.Abandon(breakerKey)
			}
			return err
		}
		businessCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		started := time.Now()
		result, submitErr := decomposer.DecomposeLayers(businessCtx, provider.LayerDecompositionRequest{
			Model: *record.ProviderModel, Image: dataURL, Prompt: valueOrPointer(record.Prompt),
			Size: valueOrPointer(record.Resolution), PromptOptimizationMode: valueOrPointer(record.PromptOptimizationMode),
		})
		cancel()
		release()
		w.recordOperationBreaker(record, breakerKey, submitErr)
		if submitErr != nil {
			w.finishAssetAttempt(attemptID, record, "submit", time.Since(started), submitErr, result.Usage, result.Telemetry)
			var providerErr *provider.Error
			if errors.As(submitErr, &providerErr) && providerErr.SubmissionUncertain {
				return river.JobCancel(w.persistOperationUncertain(record.ID, providerErr.Code, "智能分层提交结果无法确认，请联系管理员核查"))
			}
			if errors.As(submitErr, &providerErr) && providerErr.Retryable && record.RetryCount < 2 {
				return w.retryOperation(ctx, record, providerErr)
			}
			return w.failOperationAndCancel(ctx, record, providerErrorCode(submitErr), operationUserMessage(submitErr))
		}
		manifest = stagedLayerManifest{Usage: result.Usage, Items: make([]stagedLayerItem, len(result.Items))}
		for index, item := range result.Items {
			manifest.Items[index] = stagedLayerItem{
				URL: item.URL, MediaType: item.MediaType, ZIndex: item.ZIndex, BoundingBox: item.BoundingBox,
				Name: truncateUTF8(item.Name, 128), Description: truncateUTF8(item.Description, 1024),
			}
			if len(item.Bytes) > 0 {
				prepared, prepareErr := w.storeLayerBytes(ctx, record, index, item.Bytes)
				if prepareErr != nil {
					return prepareErr
				}
				manifest.Items[index].StorageKey = prepared.StorageKey
				manifest.Items[index].SHA256 = prepared.SHA256
				manifest.Items[index].ByteSize = prepared.ByteSize
				manifest.Items[index].MediaType = prepared.MediaType
				manifest.Items[index].Width = prepared.Width
				manifest.Items[index].Height = prepared.Height
			}
		}
		if err = w.persistLayerManifest(record, manifest, result.Telemetry); err != nil {
			return err
		}
		w.finishAssetAttempt(attemptID, record, "submit", time.Since(started), nil, result.Usage, result.Telemetry)
	}
	return w.ingestLayers(ctx, record, manifest)
}

func (w *AssetOperationWorker) renderAndStoreSnapshot(ctx context.Context, record assetOperationRecord) (uuid.UUID, error) {
	document, err := studioEditor.Decode(record.SourceDocument)
	if err != nil {
		return uuid.Nil, w.failOperationAndCancel(ctx, record, "INVALID_EDITOR_DOCUMENT", "当前画布无法处理，请撤销最近修改")
	}
	tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", "editor-snapshot-"+record.ID.String()+"-"+uuid.NewString()+".part.png")
	defer os.Remove(tempPath)
	if w.RenderSem != nil {
		select {
		case w.RenderSem <- struct{}{}:
			defer func() { <-w.RenderSem }()
		case <-ctx.Done():
			return uuid.Nil, ctx.Err()
		}
	}
	if err = w.renderDocument(ctx, record.OwnerID, document, tempPath); err != nil {
		return uuid.Nil, err
	}
	mediaType, extension, width, height, err := validateProviderImageFile(tempPath)
	if err != nil {
		return uuid.Nil, err
	}
	info, err := os.Stat(tempPath)
	if err != nil {
		return uuid.Nil, err
	}
	if info.Size() > maxLayerInputBytes {
		if err = encodeOpaqueJPEG(tempPath, 95); err != nil {
			return uuid.Nil, w.failOperationAndCancel(ctx, record, "EDITOR_SNAPSHOT_TOO_LARGE", "当前画布包含透明区域且快照过大，请缩小画布后重试")
		}
		mediaType, extension, width, height, err = validateProviderImageFile(tempPath)
		if err != nil {
			return uuid.Nil, err
		}
		info, err = os.Stat(tempPath)
		if err != nil {
			return uuid.Nil, err
		}
		if info.Size() > maxLayerInputBytes {
			return uuid.Nil, w.failOperationAndCancel(ctx, record, "EDITOR_SNAPSHOT_TOO_LARGE", "当前画布快照过大，请缩小画布后重试")
		}
	}
	lease := w.Blobs.AcquireContentLease()
	defer lease.Release()
	storageKey, digest, size, err := lease.PutImmutable(tempPath, extension)
	if err != nil {
		return uuid.Nil, err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer tx.Rollback(ctx)
	var assetID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size)
		VALUES($1,'derived',$2,$3,$4,$5,$6,$7) RETURNING id`, record.OwnerID, storageKey, digest, mediaType, width, height, size).Scan(&assetID)
	if err != nil {
		return uuid.Nil, err
	}
	command, err := tx.Exec(ctx, `UPDATE asset_operations SET snapshot_asset_id=$2,status='snapshotting',updated_at=now()
		WHERE id=$1 AND snapshot_asset_id IS NULL`, record.ID, assetID)
	if err != nil || command.RowsAffected() != 1 {
		return uuid.Nil, fmt.Errorf("persist operation snapshot: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return assetID, nil
}

func encodeOpaqueJPEG(path string, quality int) error {
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	decoded, _, err := image.Decode(input)
	_ = input.Close()
	if err != nil {
		return err
	}
	bounds := decoded.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			_, _, _, alpha := decoded.At(x, y).RGBA()
			if alpha != 0xffff {
				return errors.New("snapshot contains transparency")
			}
		}
	}
	temp := path + ".jpeg"
	output, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	err = jpeg.Encode(output, decoded, &jpeg.Options{Quality: quality})
	if err == nil {
		err = output.Sync()
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err = os.Remove(path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
	}
	return err
}

func (w *AssetOperationWorker) renderDocument(ctx context.Context, ownerID uuid.UUID, document studioEditor.Document, target string) error {
	canvas, err := compositeEditorDocument(ctx, document, func(assetID uuid.UUID) (image.Image, error) {
		var storageKey string
		err := w.DB.QueryRow(ctx, `SELECT storage_key FROM assets WHERE id=$1 AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false`, assetID, ownerID).Scan(&storageKey)
		if err != nil {
			return nil, err
		}
		file, err := w.Blobs.Open(storageKey)
		if err != nil {
			return nil, err
		}
		source, _, decodeErr := image.Decode(file)
		_ = file.Close()
		return source, decodeErr
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if err = png.Encode(file, canvas); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func compositeEditorDocument(ctx context.Context, document studioEditor.Document, load func(uuid.UUID) (image.Image, error)) (*image.RGBA, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, document.Canvas.Width, document.Canvas.Height))
	objects := append([]studioEditor.Object(nil), document.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return objects[i].ZIndex < objects[j].ZIndex })
	for _, object := range objects {
		if !object.Visible || object.Opacity == 0 {
			continue
		}
		source, err := load(object.AssetID)
		if err != nil {
			return nil, err
		}
		sourceBounds := source.Bounds()
		crop := sourceBounds
		if object.Crop != nil {
			crop = image.Rect(
				sourceBounds.Min.X+int(math.Round(float64(sourceBounds.Dx())*object.Crop.X)),
				sourceBounds.Min.Y+int(math.Round(float64(sourceBounds.Dy())*object.Crop.Y)),
				sourceBounds.Min.X+int(math.Round(float64(sourceBounds.Dx())*(object.Crop.X+object.Crop.Width))),
				sourceBounds.Min.Y+int(math.Round(float64(sourceBounds.Dy())*(object.Crop.Y+object.Crop.Height))),
			).Intersect(sourceBounds)
		}
		layer := image.NewRGBA(canvas.Bounds())
		matrix := f64.Aff3{object.Transform[0], object.Transform[2], object.Transform[4], object.Transform[1], object.Transform[3], object.Transform[5]}
		draw.BiLinear.Transform(layer, matrix, source, crop, draw.Src, nil)
		if object.Opacity >= 0.999 {
			stdDraw.Draw(canvas, canvas.Bounds(), layer, image.Point{}, stdDraw.Over)
		} else {
			mask := image.NewUniform(color.Alpha{A: uint8(math.Round(object.Opacity * 255))})
			stdDraw.DrawMask(canvas, canvas.Bounds(), layer, image.Point{}, mask, image.Point{}, stdDraw.Over)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	return canvas, nil
}

func (w *AssetOperationWorker) snapshotDataURL(ctx context.Context, assetID uuid.UUID) (string, error) {
	var key, mediaType string
	var size int64
	err := w.DB.QueryRow(ctx, `SELECT storage_key,media_type,byte_size FROM assets WHERE id=$1 AND kind='derived' AND purged_at IS NULL`, assetID).Scan(&key, &mediaType, &size)
	if err != nil {
		return "", err
	}
	if size > maxLayerInputBytes {
		return "", errors.New("layer snapshot exceeds provider limit")
	}
	file, err := w.Blobs.Open(key)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxLayerInputBytes+1))
	if err != nil || int64(len(data)) > maxLayerInputBytes {
		return "", errors.New("read layer snapshot")
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (w *AssetOperationWorker) persistLayerManifest(record assetOperationRecord, manifest stagedLayerManifest, telemetry provider.Telemetry) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command, err := w.DB.Exec(ctx, `UPDATE asset_operations SET staged_manifest=$2,provider_request_id=NULLIF($3,''),usage=$4,status='ingesting',updated_at=now()
		WHERE id=$1 AND status='submitting' AND staged_manifest IS NULL`, record.ID, raw, telemetry.ProviderRequestID, manifest.Usage)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("layer manifest was not persisted")
	}
	_, err = w.DB.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload)
		VALUES($1,$2,$3,'asset_operation.updated',jsonb_build_object('id',$2::uuid,'status','ingesting','editor_project_id',$3::uuid,'message',$4))`,
		record.OwnerID, record.ID, record.ProjectID, fmt.Sprintf("准备 %d 个透明图层", len(manifest.Items)-1))
	return err
}

func (w *AssetOperationWorker) ingestLayers(ctx context.Context, record assetOperationRecord, manifest stagedLayerManifest) error {
	if len(manifest.Items) < 2 || len(manifest.Items) > 17 {
		return w.failOperation(ctx, record, "LAYER_RESULT_INVALID", "智能分层没有返回可用图层")
	}
	prepared := make([]stagedLayerItem, len(manifest.Items))
	var groupBytes atomic.Int64
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	for index, item := range manifest.Items {
		index, item := index, item
		group.Go(func() error {
			if item.StorageKey == "" {
				tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", fmt.Sprintf("layer-%s-%d-%s.part", record.ID, index, uuid.NewString()))
				defer os.Remove(tempPath)
				attemptID, err := w.beginAssetAttempt(groupCtx, record, fmt.Sprintf("download_%d", index))
				if err != nil {
					return err
				}
				started := time.Now()
				err = w.downloadLayer(groupCtx, item.URL, tempPath)
				w.finishAssetAttempt(attemptID, record, fmt.Sprintf("download_%d", index), time.Since(started), err, nil, provider.Telemetry{})
				if err != nil {
					return err
				}
				stored, err := w.storeLayerFile(groupCtx, tempPath)
				if err != nil {
					return err
				}
				item.StorageKey, item.SHA256, item.ByteSize, item.MediaType, item.Width, item.Height = stored.StorageKey, stored.SHA256, stored.ByteSize, stored.MediaType, stored.Width, stored.Height
			}
			if groupBytes.Add(item.ByteSize) > maxLayerGroupBytes {
				return errors.New("layer output group exceeds 512 MiB")
			}
			if item.BlurDataURL == "" {
				blur, err := w.Generator.ensurePresentationVariants(groupCtx, item.StorageKey)
				if err != nil {
					return err
				}
				item.BlurDataURL = blur
			}
			item.URL = ""
			prepared[index] = item
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	manifest.Items = prepared
	if err := w.persistPreparedLayerManifest(ctx, record.ID, manifest); err != nil {
		return err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM asset_operations WHERE id=$1 FOR UPDATE`, record.ID).Scan(&status); err != nil {
		return err
	}
	if status == "succeeded" {
		return tx.Commit(ctx)
	}
	if status != "ingesting" {
		return fmt.Errorf("cannot ingest layer operation in status %s", status)
	}
	assetIDs := make(map[int]uuid.UUID, len(prepared))
	for _, item := range prepared {
		var assetID uuid.UUID
		err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size,blur_data_url)
			VALUES($1,'derived',$2,$3,$4,$5,$6,$7,$8) RETURNING id`, record.OwnerID, item.StorageKey, item.SHA256, item.MediaType, item.Width, item.Height, item.ByteSize, item.BlurDataURL).Scan(&assetID)
		if err != nil {
			return err
		}
		assetIDs[item.ZIndex] = assetID
	}
	baseID, ok := assetIDs[0]
	if !ok {
		return errors.New("layer result has no base asset")
	}
	var layerSetID uuid.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO layer_sets(owner_user_id,editor_project_id,asset_operation_id,source_revision,base_asset_id)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, record.OwnerID, record.ProjectID, record.ID, record.SourceRevision, baseID).Scan(&layerSetID); err != nil {
		return err
	}
	for _, item := range prepared {
		if item.ZIndex == 0 {
			continue
		}
		if item.BoundingBox == nil {
			return errors.New("layer result is missing bounding box")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO layer_set_items(layer_set_id,asset_id,z_index,name,description,bbox_absolute,bbox_normalized)
			VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7)`, layerSetID, assetIDs[item.ZIndex], item.ZIndex, item.Name, item.Description,
			item.BoundingBox.Absolute[:], item.BoundingBox.Normalized[:]); err != nil {
			return err
		}
	}
	command, err := tx.Exec(ctx, `UPDATE image_editor_projects SET active_layer_set_id=$3,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$4`, record.ProjectID, record.OwnerID, layerSetID, record.SourceRevision)
	if err != nil {
		return err
	}
	applied := command.RowsAffected() == 1
	if _, err = tx.Exec(ctx, `UPDATE asset_operations SET status='succeeded',dispatch_state='finished',staged_manifest=NULL,completed_at=now(),updated_at=now(),error_code=NULL,error_message=NULL
		WHERE id=$1`, record.ID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"id": record.ID, "status": "succeeded", "editor_project_id": record.ProjectID, "layer_set_id": layerSetID, "layer_count": len(prepared) - 1, "applied": applied})
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload)
		VALUES($1,$2,$3,'asset_operation.succeeded',$4)`, record.OwnerID, record.ID, record.ProjectID, payload); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	for _, item := range prepared {
		w.Generator.queueOptionalThumbnail(item.StorageKey)
	}
	return nil
}

func (w *AssetOperationWorker) persistPreparedLayerManifest(ctx context.Context, operationID uuid.UUID, manifest stagedLayerManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	command, err := w.DB.Exec(writeCtx, `UPDATE asset_operations SET staged_manifest=$2,updated_at=now()
		WHERE id=$1 AND status='ingesting'`, operationID, raw)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("layer ingest manifest lost its state lease")
	}
	return nil
}

func (w *AssetOperationWorker) publishEditorDocument(ctx context.Context, record assetOperationRecord) error {
	document, err := studioEditor.Decode(record.SourceDocument)
	if err != nil {
		return w.failOperation(ctx, record, "INVALID_EDITOR_DOCUMENT", "当前画布无法发布")
	}
	if err = w.setOperationStatus(ctx, record, "snapshotting", "正在保存为新图片"); err != nil {
		return err
	}
	tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", "editor-publish-"+record.ID.String()+"-"+uuid.NewString()+".part.png")
	defer os.Remove(tempPath)
	if err = w.renderDocument(ctx, record.OwnerID, document, tempPath); err != nil {
		return err
	}
	stored, err := w.storeLayerFile(ctx, tempPath)
	if err != nil {
		return err
	}
	blur, err := w.Generator.ensurePresentationVariants(ctx, stored.StorageKey)
	if err != nil {
		return err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var assetID uuid.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,original_filename,width,height,byte_size,blur_data_url)
		VALUES($1,'editor',$2,$3,$4,'Cornfield 编辑.png',$5,$6,$7,$8) RETURNING id`, record.OwnerID, stored.StorageKey, stored.SHA256, stored.MediaType, stored.Width, stored.Height, stored.ByteSize, blur).Scan(&assetID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE asset_operations SET status='succeeded',dispatch_state='finished',result_asset_id=$2,completed_at=now(),updated_at=now() WHERE id=$1`, record.ID, assetID); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"id": record.ID, "status": "succeeded", "editor_project_id": record.ProjectID, "asset_id": assetID})
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload) VALUES($1,$2,$3,'asset_operation.succeeded',$4)`, record.OwnerID, record.ID, record.ProjectID, payload); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	w.Generator.queueOptionalThumbnail(stored.StorageKey)
	return nil
}

func (w *AssetOperationWorker) packageLayerSet(ctx context.Context, record assetOperationRecord) error {
	if record.LayerSetID == nil {
		return w.failOperation(ctx, record, "LAYER_SET_NOT_FOUND", "没有可打包的图层")
	}
	layerSetID := *record.LayerSetID
	var existingPackage uuid.UUID
	err := w.DB.QueryRow(ctx, `SELECT package_asset_id FROM layer_sets WHERE id=$1 AND package_ready_at IS NOT NULL`, layerSetID).Scan(&existingPackage)
	if err == nil {
		return w.setOperationSucceeded(ctx, record, map[string]any{"layer_set_id": layerSetID, "package_ready": true})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", "layers-"+layerSetID.String()+"-"+uuid.NewString()+".part.zip")
	defer os.Remove(tempPath)
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	rows, err := w.DB.Query(ctx, `SELECT storage_key,name FROM (
		SELECT a.storage_key,'00-background'::text AS name
		FROM layer_sets s JOIN assets a ON a.id=s.base_asset_id WHERE s.id=$1
		UNION ALL
		SELECT a.storage_key,lpad(i.z_index::text,2,'0')||'-'||i.name
		FROM layer_set_items i JOIN assets a ON a.id=i.asset_id WHERE i.layer_set_id=$1
	) files ORDER BY name`, layerSetID)
	if err != nil {
		_ = archive.Close()
		_ = file.Close()
		return err
	}
	for rows.Next() {
		var key, name string
		if err = rows.Scan(&key, &name); err != nil {
			break
		}
		input, openErr := w.Blobs.Open(key)
		if openErr != nil {
			err = openErr
			break
		}
		entry, createErr := archive.Create(safeArchiveName(name) + filepath.Ext(key))
		if createErr == nil {
			_, createErr = io.Copy(entry, input)
		}
		_ = input.Close()
		if createErr != nil {
			err = createErr
			break
		}
	}
	rows.Close()
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if syncErr := file.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	lease := w.Blobs.AcquireContentLease()
	defer lease.Release()
	key, digest, size, err := lease.PutImmutable(tempPath, "zip")
	if err != nil {
		return err
	}
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var assetID uuid.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,original_filename,width,height,byte_size)
		VALUES($1,'derived',$2,$3,'application/zip','Cornfield 图层.zip',1,1,$4) RETURNING id`, record.OwnerID, key, digest, size).Scan(&assetID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE layer_sets SET package_asset_id=$2,package_ready_at=now() WHERE id=$1 AND package_asset_id IS NULL`, layerSetID, assetID)
	if err != nil || command.RowsAffected() != 1 {
		return fmt.Errorf("persist layer package: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return w.setOperationSucceeded(ctx, record, map[string]any{"layer_set_id": layerSetID, "package_ready": true})
}

func safeArchiveName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '-'
		}
		return r
	}, value)
	value = strings.Trim(strings.TrimSpace(value), ".")
	if value == "" {
		return "layer"
	}
	return truncateUTF8(value, 96)
}

func (w *AssetOperationWorker) storeLayerBytes(ctx context.Context, record assetOperationRecord, index int, data []byte) (stagedLayerItem, error) {
	tempPath := filepath.Join(w.Config.AssetRoot, "uploads", "tmp", fmt.Sprintf("layer-mock-%s-%d-%s.part", record.ID, index, uuid.NewString()))
	defer os.Remove(tempPath)
	if err := writeSynced(tempPath, data); err != nil {
		return stagedLayerItem{}, err
	}
	return w.storeLayerFile(ctx, tempPath)
}

func (w *AssetOperationWorker) storeLayerFile(ctx context.Context, tempPath string) (stagedLayerItem, error) {
	mediaType, extension, width, height, err := validateProviderImageFile(tempPath)
	if err != nil {
		return stagedLayerItem{}, err
	}
	lease := w.Blobs.AcquireContentLease()
	defer lease.Release()
	key, digest, size, err := lease.PutImmutable(tempPath, extension)
	if err != nil {
		return stagedLayerItem{}, err
	}
	return stagedLayerItem{StorageKey: key, SHA256: digest, ByteSize: size, MediaType: mediaType, Width: width, Height: height}, nil
}

func (w *AssetOperationWorker) downloadLayer(ctx context.Context, rawURL, target string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || !bytePlusLayerURLAllowed(parsed) {
		return errors.New("layer output URL rejected")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		retryAfter, lastErr := w.downloadLayerOnce(ctx, parsed, target)
		if lastErr == nil {
			return nil
		}
		if retryAfter < 0 {
			return lastErr
		}
		if attempt < 2 {
			if retryAfter <= 0 {
				ceiling := 250 * time.Millisecond * time.Duration(1<<attempt)
				retryAfter = time.Duration(rand.Int64N(int64(ceiling) + 1))
			}
			timer := time.NewTimer(retryAfter)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

func (w *AssetOperationWorker) downloadLayerOnce(ctx context.Context, initial *url.URL, target string) (time.Duration, error) {
	current := initial
	for redirects := 0; redirects <= 3; redirects++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		res, err := w.HTTPClient.Do(req)
		if err != nil {
			return 0, errors.New("layer output download failed")
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			location := res.Header.Get("Location")
			res.Body.Close()
			next, parseErr := current.Parse(location)
			if parseErr != nil || !bytePlusLayerURLAllowed(next) || redirects == 3 {
				return 0, errors.New("layer output redirect rejected")
			}
			current = next
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			status := res.StatusCode
			retryAfter := parseDownloadRetryAfter(res.Header.Get("Retry-After"), time.Now())
			res.Body.Close()
			if status != http.StatusRequestTimeout && status != http.StatusTooManyRequests && status < 500 {
				return -1, fmt.Errorf("layer output HTTP %d", status)
			}
			return retryAfter, fmt.Errorf("layer output HTTP %d", status)
		}
		file, openErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if openErr != nil {
			res.Body.Close()
			return -1, openErr
		}
		written, copyErr := io.Copy(file, io.LimitReader(res.Body, 50<<20+1))
		res.Body.Close()
		if syncErr := file.Sync(); copyErr == nil {
			copyErr = syncErr
		}
		if closeErr := file.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil && written <= 50<<20 {
			return 0, nil
		}
		_ = os.Remove(target)
		if written > 50<<20 {
			return -1, errors.New("layer output exceeds 50 MiB")
		}
		return 0, errors.New("layer output download failed")
	}
	return -1, errors.New("layer output redirect rejected")
}

func bytePlusLayerURLAllowed(value *url.URL) bool {
	if value == nil || value.Scheme != "https" || value.User != nil || value.Hostname() == "" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
	return host == "bytepluses.com" || strings.HasSuffix(host, ".bytepluses.com") || host == "byteplus.com" || strings.HasSuffix(host, ".byteplus.com")
}

func (w *AssetOperationWorker) beginAssetAttempt(ctx context.Context, record assetOperationRecord, operation string) (int64, error) {
	var id int64
	providerID := valueOrPointer(record.ProviderID)
	err := w.DB.QueryRow(ctx, `INSERT INTO provider_attempts(asset_operation_id,provider_id,operation,attempt_no,outcome)
		VALUES($1,$2,$3,(SELECT count(*)+1 FROM provider_attempts WHERE asset_operation_id=$1 AND operation=$3),'started') RETURNING id`, record.ID, providerID, operation).Scan(&id)
	return id, err
}

func (w *AssetOperationWorker) finishAssetAttempt(id int64, record assetOperationRecord, operation string, duration time.Duration, attemptErr error, usage map[string]any, telemetry provider.Telemetry) {
	outcome, code, message := "succeeded", "", ""
	if attemptErr != nil {
		outcome, message = "failed", boundedAttemptMessage(attemptErr.Error())
		code = providerErrorCode(attemptErr)
		var providerErr *provider.Error
		if errors.As(attemptErr, &providerErr) {
			telemetry = providerErr.Telemetry
		}
	}
	telemetry = telemetry.Normalized()
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := w.DB.Exec(writeCtx, `UPDATE provider_attempts SET provider_request_id=NULLIF($2,''),http_status=NULLIF($3,0),duration_ms=$4,outcome=$5,
		error_code=NULLIF($6,''),error_message=NULLIF($7,''),usage=COALESCE($8,'{}'::jsonb),finished_at=now()
		WHERE id=$1 AND asset_operation_id=$9 AND operation=$10 AND outcome='started'`, id, telemetry.ProviderRequestID, telemetry.HTTPStatus,
		duration.Milliseconds(), outcome, code, message, sanitizeAttemptUsage(usage), record.ID, operation)
	if err != nil && w.Log != nil {
		w.Log.Warn("asset operation attempt persistence failed", "asset_operation_id", record.ID, "operation", operation, "error", err)
	}
}

func (w *AssetOperationWorker) acquireOperationProvider(ctx context.Context, providerID string) (func(), error) {
	semaphore := w.ProviderSem[providerID]
	if semaphore == nil {
		return func() {}, nil
	}
	select {
	case semaphore <- struct{}{}:
		return func() { <-semaphore }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *AssetOperationWorker) deferOperationForBreaker(ctx context.Context, record assetOperationRecord) error {
	delay := 30 * time.Second
	if w.Catalog != nil && record.ModelID != nil {
		if model, ok := w.Catalog.Find(*record.ModelID); ok && model.Policy.BreakerCooldownSeconds > 0 {
			delay = time.Duration(model.Policy.BreakerCooldownSeconds) * time.Second
		}
	}
	_, err := w.DB.Exec(ctx, `UPDATE asset_operations SET status='queued',dispatch_state='pending',river_job_id=NULL,
		next_attempt_at=now()+$2::interval,updated_at=now() WHERE id=$1`, record.ID, delay.String())
	if err != nil {
		return err
	}
	return river.JobCancel(errors.New("provider circuit breaker is open"))
}

func (w *AssetOperationWorker) recordOperationBreaker(record assetOperationRecord, key string, attemptErr error) {
	if w.Generator == nil || w.Generator.Breaker == nil || record.ProviderID == nil {
		return
	}
	if breakerExemptError(attemptErr) {
		w.Generator.Breaker.Abandon(key)
		return
	}
	minimum, ratio, cooldown := 10, 0.5, 30*time.Second
	if w.Catalog != nil && record.ModelID != nil {
		if model, ok := w.Catalog.Find(*record.ModelID); ok {
			minimum = model.Policy.BreakerMinRequests
			ratio = model.Policy.BreakerFailureRatio
			cooldown = time.Duration(model.Policy.BreakerCooldownSeconds) * time.Second
		}
	}
	opened, until := w.Generator.Breaker.RecordPolicy(key, attemptErr == nil, minimum, ratio, cooldown)
	writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if opened {
		_, _ = w.DB.Exec(writeCtx, `UPDATE providers SET state='degraded',breaker_open_until=$2,last_error_code=$3,last_error_at=now(),updated_at=now()
			WHERE id=$1 AND state<>'paused'`, *record.ProviderID, until, providerErrorCode(attemptErr))
	} else if attemptErr == nil {
		_, _ = w.DB.Exec(writeCtx, `UPDATE providers SET state='healthy',breaker_open_until=NULL,last_error_code=NULL,updated_at=now()
			WHERE id=$1 AND state<>'paused'`, *record.ProviderID)
	}
	var providerErr *provider.Error
	if !errors.As(attemptErr, &providerErr) || !providerErr.PauseProvider {
		return
	}
	tx, err := w.DB.Begin(writeCtx)
	if err != nil {
		return
	}
	defer tx.Rollback(writeCtx)
	if _, err = tx.Exec(writeCtx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "provider-slot:"+*record.ProviderID); err == nil {
		_, err = tx.Exec(writeCtx, `UPDATE providers SET state='paused',last_error_code=$2,last_error_at=now(),
			last_probe_state='unknown',last_probe_error_code=NULL,updated_at=now() WHERE id=$1`, *record.ProviderID, providerErr.Code)
	}
	if err == nil {
		err = failUnavailableProviderJobsInTx(writeCtx, tx, *record.ProviderID)
	}
	if err == nil {
		err = tx.Commit(writeCtx)
	}
	if err != nil && w.Log != nil {
		w.Log.Error("asset operation provider pause could not be persisted", "provider", *record.ProviderID, "error", err)
	}
}

func (w *AssetOperationWorker) setOperationStatus(ctx context.Context, record assetOperationRecord, status, message string) error {
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE asset_operations SET status=$2,updated_at=now() WHERE id=$1`, record.ID, status); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"id": record.ID, "status": status, "editor_project_id": record.ProjectID, "message": message})
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload) VALUES($1,$2,$3,'asset_operation.updated',$4)`, record.OwnerID, record.ID, record.ProjectID, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *AssetOperationWorker) failOperation(ctx context.Context, record assetOperationRecord, code, message string) error {
	_, err := w.DB.Exec(ctx, `UPDATE asset_operations SET status='failed',dispatch_state='finished',error_code=$2,error_message=$3,completed_at=now(),updated_at=now() WHERE id=$1 AND status NOT IN ('succeeded','cancelled')`, record.ID, code, message)
	if err == nil {
		payload, _ := json.Marshal(map[string]any{"id": record.ID, "status": "failed", "editor_project_id": record.ProjectID, "error_code": code, "error_message": message})
		_, err = w.DB.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload) VALUES($1,$2,$3,'asset_operation.failed',$4)`, record.OwnerID, record.ID, record.ProjectID, payload)
	}
	return err
}

func (w *AssetOperationWorker) failOperationAndCancel(ctx context.Context, record assetOperationRecord, code, message string) error {
	if err := w.failOperation(ctx, record, code, message); err != nil {
		return err
	}
	return river.JobCancel(errors.New(code))
}

func (w *AssetOperationWorker) setOperationSucceeded(ctx context.Context, record assetOperationRecord, extra map[string]any) error {
	_, err := w.DB.Exec(ctx, `UPDATE asset_operations SET status='succeeded',dispatch_state='finished',completed_at=now(),updated_at=now() WHERE id=$1`, record.ID)
	if err != nil {
		return err
	}
	extra["id"], extra["status"], extra["editor_project_id"] = record.ID, "succeeded", record.ProjectID
	payload, _ := json.Marshal(extra)
	_, err = w.DB.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload) VALUES($1,$2,$3,'asset_operation.succeeded',$4)`, record.OwnerID, record.ID, record.ProjectID, payload)
	return err
}

func (w *AssetOperationWorker) persistOperationUncertain(id uuid.UUID, code, message string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := w.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ownerID, projectID uuid.UUID
	var status string
	err = tx.QueryRow(ctx, `SELECT owner_user_id,editor_project_id,status FROM asset_operations WHERE id=$1 FOR UPDATE`, id).Scan(&ownerID, &projectID, &status)
	if err != nil {
		return err
	}
	if operationTerminal(status) {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE asset_operations SET status='submission_uncertain',submission_uncertain=true,dispatch_state='finished',error_code=$2,error_message=$3,completed_at=now(),updated_at=now() WHERE id=$1`, id, code, message); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"id": id, "status": "submission_uncertain", "editor_project_id": projectID, "error_code": code, "error_message": message})
	if _, err = tx.Exec(ctx, `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload) VALUES($1,$2,$3,'asset_operation.submission_uncertain',$4)`, ownerID, id, projectID, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *AssetOperationWorker) hasUnfinishedSubmitAttempt(id uuid.UUID) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exists bool
	err := w.DB.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM asset_operations o
		JOIN provider_attempts a ON a.asset_operation_id=o.id
		WHERE o.id=$1 AND o.operation_type='layer_decomposition'
		  AND o.status='submitting' AND a.operation='submit' AND a.finished_at IS NULL
	)`, id).Scan(&exists)
	return exists, err
}

// RunSubmissionRecovery closes the only non-idempotent crash window. It is a
// fallback for a process killed after a paid request may have left the host but
// before its response was durably stored. Such work is never auto-resubmitted.
func (w *AssetOperationWorker) RunSubmissionRecovery(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		w.recoverStaleSubmissions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *AssetOperationWorker) recoverStaleSubmissions(ctx context.Context) {
	rows, err := w.DB.Query(ctx, `SELECT DISTINCT o.id FROM asset_operations o
		JOIN provider_attempts a ON a.asset_operation_id=o.id
		WHERE o.operation_type='layer_decomposition' AND o.status='submitting'
		  AND a.operation='submit' AND a.finished_at IS NULL
		  AND a.created_at < now()-interval '11 minutes' LIMIT 100`)
	if err != nil {
		w.Log.Warn("asset operation submission recovery scan failed", "error", err)
		return
	}
	ids := make([]uuid.UUID, 0, 100)
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			rows.Close()
			w.Log.Warn("asset operation submission recovery scan failed", "error", scanErr)
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		w.Log.Warn("asset operation submission recovery scan failed", "error", err)
		return
	}
	for _, id := range ids {
		if persistErr := w.persistOperationUncertain(id, "SUBMISSION_UNCERTAIN", "智能分层提交结果无法确认，请联系管理员核查"); persistErr != nil {
			w.Log.Warn("asset operation submission recovery failed", "asset_operation_id", id, "error", persistErr)
		}
	}
}

func (w *AssetOperationWorker) retryOperation(ctx context.Context, record assetOperationRecord, providerErr *provider.Error) error {
	delay := providerErr.RetryAfter
	if delay <= 0 {
		delay = time.Duration(record.RetryCount+1) * time.Second
	}
	_, err := w.DB.Exec(ctx, `UPDATE asset_operations SET status='queued',dispatch_state='pending',river_job_id=NULL,retry_count=retry_count+1,next_attempt_at=now()+$2::interval,updated_at=now() WHERE id=$1`, record.ID, delay.String())
	if err != nil {
		return err
	}
	return river.JobCancel(providerErr)
}

func operationUserMessage(err error) string {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Code {
		case "CONTENT_POLICY_REJECTED":
			return "图片可能触发安全策略，请调整内容后重试"
		case "PROVIDER_HTTP_400", "PROVIDER_HTTP_422", "UNSUPPORTED_PARAMETER":
			return "当前画布或分层参数无法处理，请调整后重试"
		case "PROVIDER_HTTP_429":
			return "智能分层服务繁忙，请稍后重试"
		}
	}
	return "智能分层失败，请稍后重试"
}

func truncateUTF8(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum])
	}
	return value
}
