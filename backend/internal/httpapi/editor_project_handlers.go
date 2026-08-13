package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	studioEditor "internal-image-studio/internal/editor"
)

const (
	maxEditorProjectName = 64
	maxActiveEditorOps   = 1
	maxQueuedEditorOps   = 10
)

type editorProjectResponse struct {
	ID                uuid.UUID         `json:"id"`
	SourceAssetID     uuid.UUID         `json:"source_asset_id"`
	Name              string            `json:"name"`
	Document          json.RawMessage   `json:"document"`
	Revision          int64             `json:"revision"`
	ActiveLayerSetID  *uuid.UUID        `json:"active_layer_set_id,omitempty"`
	ActiveLayerSet    *layerSetResponse `json:"active_layer_set,omitempty"`
	LatestOperationID *uuid.UUID        `json:"latest_operation_id,omitempty"`
	CreatedAt         string            `json:"created_at"`
	UpdatedAt         string            `json:"updated_at"`
}

type assetOperationResponse struct {
	ID                     uuid.UUID         `json:"id"`
	ProjectID              uuid.UUID         `json:"editor_project_id"`
	Type                   string            `json:"operation_type"`
	Status                 string            `json:"status"`
	SourceRevision         int64             `json:"source_revision"`
	Resolution             *string           `json:"resolution,omitempty"`
	PromptOptimizationMode *string           `json:"prompt_optimization_mode,omitempty"`
	ErrorCode              *string           `json:"error_code,omitempty"`
	ErrorMessage           *string           `json:"error_message,omitempty"`
	SubmissionUncertain    bool              `json:"submission_uncertain"`
	ResultAssetID          *uuid.UUID        `json:"result_asset_id,omitempty"`
	LayerSet               *layerSetResponse `json:"layer_set,omitempty"`
	StartedAt              *string           `json:"started_at,omitempty"`
	CreatedAt              string            `json:"created_at"`
	UpdatedAt              string            `json:"updated_at"`
}

type layerSetResponse struct {
	ID               uuid.UUID           `json:"id"`
	SourceRevision   int64               `json:"source_revision"`
	BaseAsset        assetResponse       `json:"base_asset"`
	Items            []layerItemResponse `json:"items"`
	PackageReady     bool                `json:"package_ready"`
	AppliedToProject bool                `json:"applied_to_project"`
}

type layerItemResponse struct {
	ID                    uuid.UUID     `json:"id"`
	ZIndex                int           `json:"z_index"`
	Name                  string        `json:"name"`
	Description           *string       `json:"description,omitempty"`
	BoundingBoxAbsolute   []int         `json:"bounding_box_absolute"`
	BoundingBoxNormalized []float64     `json:"bounding_box_normalized"`
	Asset                 assetResponse `json:"asset"`
}

func editorTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func (s *Server) getOrCreateEditorProject(w http.ResponseWriter, r *http.Request) {
	assetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_CREATE_FAILED", "无法打开图片工作台", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var width, height int
	var filename *string
	err = tx.QueryRow(r.Context(), `SELECT width,height,original_filename FROM assets
		WHERE id=$1 AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false AND kind<>'derived' FOR SHARE`, assetID, sess.UserID).Scan(&width, &height, &filename)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "图片不存在或不可编辑", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_CREATE_FAILED", "无法打开图片工作台", true, r)
		return
	}
	document, err := json.Marshal(studioEditor.New(assetID, width, height))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_CREATE_FAILED", "无法创建编辑工程", true, r)
		return
	}
	name := "未命名图片"
	if filename != nil {
		candidate := strings.TrimSpace(strings.TrimSuffix(*filename, filepath.Ext(*filename)))
		if candidate != "" {
			name = truncateRunes(candidate, maxEditorProjectName)
		}
	}
	var item editorProjectResponse
	var createdAt, updatedAt time.Time
	var created bool
	err = tx.QueryRow(r.Context(), `WITH inserted AS (
		INSERT INTO image_editor_projects(owner_user_id,source_asset_id,name,document)
		VALUES($1,$2,$3,$4) ON CONFLICT(owner_user_id,source_asset_id) DO NOTHING
		RETURNING id,source_asset_id,name,document,revision,active_layer_set_id,created_at,updated_at,true AS created
	) SELECT * FROM inserted
	UNION ALL
	SELECT id,source_asset_id,name,document,revision,active_layer_set_id,created_at,updated_at,false
	FROM image_editor_projects WHERE owner_user_id=$1 AND source_asset_id=$2
	LIMIT 1`, sess.UserID, assetID, name, document).Scan(
		&item.ID, &item.SourceAssetID, &item.Name, &item.Document, &item.Revision,
		&item.ActiveLayerSetID, &createdAt, &updatedAt, &created,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_CREATE_FAILED", "无法打开图片工作台", true, r)
		return
	}
	item.CreatedAt, item.UpdatedAt = editorTime(createdAt), editorTime(updatedAt)
	_ = tx.QueryRow(r.Context(), `SELECT id FROM asset_operations WHERE editor_project_id=$1 AND operation_type<>'layer_package' ORDER BY created_at DESC LIMIT 1`, item.ID).Scan(&item.LatestOperationID)
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_CREATE_FAILED", "无法打开图片工作台", true, r)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (s *Server) getEditorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	item, err := s.loadEditorProject(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "EDITOR_PROJECT_NOT_FOUND", "图片编辑工程不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取图片编辑工程失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) loadEditorProject(r *http.Request, id uuid.UUID) (editorProjectResponse, error) {
	var item editorProjectResponse
	var createdAt, updatedAt time.Time
	err := s.db.QueryRow(r.Context(), `SELECT p.id,p.source_asset_id,p.name,p.document,p.revision,p.active_layer_set_id,p.created_at,p.updated_at,
		(SELECT o.id FROM asset_operations o WHERE o.editor_project_id=p.id AND o.operation_type<>'layer_package' ORDER BY o.created_at DESC LIMIT 1)
		FROM image_editor_projects p WHERE p.id=$1 AND p.owner_user_id=$2`, id, currentSession(r).UserID).Scan(
		&item.ID, &item.SourceAssetID, &item.Name, &item.Document, &item.Revision,
		&item.ActiveLayerSetID, &createdAt, &updatedAt, &item.LatestOperationID,
	)
	item.CreatedAt, item.UpdatedAt = editorTime(createdAt), editorTime(updatedAt)
	if item.ActiveLayerSetID != nil {
		var operationID uuid.UUID
		var sourceRevision int64
		if lookupErr := s.db.QueryRow(r.Context(), `SELECT asset_operation_id,source_revision FROM layer_sets
			WHERE id=$1 AND owner_user_id=$2`, *item.ActiveLayerSetID, currentSession(r).UserID).Scan(&operationID, &sourceRevision); lookupErr == nil {
			item.ActiveLayerSet, _ = s.loadLayerSet(r, operationID, item.ID, sourceRevision)
		}
	}
	return item, err
}

func (s *Server) saveEditorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64           `json:"expected_revision"`
		Document         json.RawMessage `json:"document"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Document) > studioEditor.MaxDocumentBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "EDITOR_DOCUMENT_TOO_LARGE", "编辑工程不能超过 256 KiB", false, r)
		return
	}
	document, err := studioEditor.Decode(input.Document)
	if err != nil || input.ExpectedRevision < 0 {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_EDITOR_DOCUMENT", "编辑工程结构无效，请撤销最近的修改", false, r)
		return
	}
	if err = s.requireOwnedEditorAssets(r, document.AssetIDs()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "EDITOR_ASSET_INVALID", "工程包含不存在或不可访问的图片", false, r)
		return
	}
	var revision int64
	err = s.db.QueryRow(r.Context(), `UPDATE image_editor_projects SET document=$4,revision=revision+1,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$3 RETURNING revision`, id, currentSession(r).UserID, input.ExpectedRevision, input.Document).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "EDITOR_PROJECT_CONFLICT", "工程已在其他页面更新，请刷新后继续", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_SAVE_FAILED", "保存编辑工程失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (s *Server) requireOwnedEditorAssets(r *http.Request, assetIDs []uuid.UUID) error {
	var count int
	err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM assets
		WHERE owner_user_id=$1 AND id=ANY($2) AND purged_at IS NULL AND purge_pending=false`, currentSession(r).UserID, assetIDs).Scan(&count)
	if err != nil || count != len(assetIDs) {
		return errors.New("editor asset unavailable")
	}
	return nil
}

func (s *Server) renameEditorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !utf8.ValidString(input.Name) || utf8.RuneCountInString(input.Name) < 1 || utf8.RuneCountInString(input.Name) > maxEditorProjectName {
		writeError(w, http.StatusUnprocessableEntity, "EDITOR_PROJECT_NAME_INVALID", "工程名称需要 1–64 个字符", false, r)
		return
	}
	command, err := s.db.Exec(r.Context(), `UPDATE image_editor_projects SET name=$3,updated_at=now() WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID, input.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_UPDATE_FAILED", "更新工程名称失败", true, r)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "EDITOR_PROJECT_NOT_FOUND", "图片编辑工程不存在", false, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteEditorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var active int
	err = tx.QueryRow(r.Context(), `SELECT count(o.id) FROM image_editor_projects p
		LEFT JOIN asset_operations o ON o.editor_project_id=p.id AND o.status NOT IN ('succeeded','failed','cancelled','submission_uncertain')
		WHERE p.id=$1 AND p.owner_user_id=$2 GROUP BY p.id FOR UPDATE OF p`, id, currentSession(r).UserID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "EDITOR_PROJECT_NOT_FOUND", "图片编辑工程不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	if active > 0 {
		writeError(w, http.StatusConflict, "EDITOR_PROJECT_BUSY", "分层任务仍在运行，暂时无法删除工程", false, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT DISTINCT asset_id FROM (
		SELECT snapshot_asset_id AS asset_id FROM asset_operations WHERE editor_project_id=$1
		UNION ALL SELECT base_asset_id FROM layer_sets WHERE editor_project_id=$1
		UNION ALL SELECT package_asset_id FROM layer_sets WHERE editor_project_id=$1
		UNION ALL SELECT i.asset_id FROM layer_set_items i JOIN layer_sets s ON s.id=i.layer_set_id WHERE s.editor_project_id=$1
	) refs WHERE asset_id IS NOT NULL`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	derived := make([]uuid.UUID, 0)
	for rows.Next() {
		var assetID uuid.UUID
		if err = rows.Scan(&assetID); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
			return
		}
		derived = append(derived, assetID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	for _, assetID := range derived {
		if _, err = tx.Exec(r.Context(), `UPDATE assets SET purge_pending=true WHERE id=$1 AND owner_user_id=$2 AND kind='derived' AND purged_at IS NULL`, assetID, currentSession(r).UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,asset_id,requested_by)
			SELECT 'asset',$2,$1,$2 WHERE NOT EXISTS(SELECT 1 FROM deletion_requests WHERE asset_id=$1 AND status IN ('pending','running'))`, assetID, currentSession(r).UserID); err != nil {
			writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
			return
		}
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM image_editor_projects WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PROJECT_DELETE_FAILED", "删除编辑工程失败", true, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createLayerDecomposition(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision       int64  `json:"expected_revision"`
		Prompt                 string `json:"prompt"`
		Resolution             string `json:"resolution"`
		PromptOptimizationMode string `json:"prompt_optimization_mode"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Resolution == "" {
		input.Resolution = "auto"
	}
	if input.PromptOptimizationMode == "" {
		input.PromptOptimizationMode = "standard"
	}
	model, ok := s.catalog.Find("byteplus-seedream-5-0-pro")
	if !ok || !model.Enabled || !model.Capabilities.LayerDecomposition {
		writeError(w, http.StatusServiceUnavailable, "LAYER_DECOMPOSITION_DISABLED", "智能分层正在灰度准备中", true, r)
		return
	}
	if !slicesString(model.Capabilities.LayerDecompositionSizes, input.Resolution) || !slicesString(model.Capabilities.PromptOptimizationModes, input.PromptOptimizationMode) || utf8.RuneCountInString(input.Prompt) > 8192 {
		writeError(w, http.StatusUnprocessableEntity, "LAYER_DECOMPOSITION_OPTIONS_INVALID", "智能分层参数无效", false, r)
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "需要有效的 Idempotency-Key", false, r)
		return
	}
	hashBytes, _ := json.Marshal(struct {
		ProjectID uuid.UUID `json:"project_id"`
		Input     any       `json:"input"`
	}{ProjectID: projectID, Input: input})
	hash := sha256.Sum256(hashBytes)
	requestHash := hex.EncodeToString(hash[:])
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法创建智能分层任务", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var existing assetOperationResponse
	var createdAt, updatedAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT id,editor_project_id,operation_type,status,source_revision,resolution,prompt_optimization_mode,error_code,error_message,submission_uncertain,created_at,updated_at
		FROM asset_operations WHERE owner_user_id=$1 AND idempotency_key=$2`, currentSession(r).UserID, idempotencyKey).Scan(
		&existing.ID, &existing.ProjectID, &existing.Type, &existing.Status, &existing.SourceRevision,
		&existing.Resolution, &existing.PromptOptimizationMode, &existing.ErrorCode, &existing.ErrorMessage,
		&existing.SubmissionUncertain, &createdAt, &updatedAt,
	)
	if err == nil {
		var existingHash string
		if scanErr := tx.QueryRow(r.Context(), `SELECT request_hash FROM asset_operations WHERE id=$1`, existing.ID).Scan(&existingHash); scanErr != nil || existingHash != requestHash {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key 已用于其他参数", false, r)
			return
		}
		existing.CreatedAt, existing.UpdatedAt = editorTime(createdAt), editorTime(updatedAt)
		_ = tx.Commit(r.Context())
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法创建智能分层任务", true, r)
		return
	}
	var currentRevision int64
	err = tx.QueryRow(r.Context(), `SELECT revision FROM image_editor_projects WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, projectID, currentSession(r).UserID).Scan(&currentRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "EDITOR_PROJECT_NOT_FOUND", "图片编辑工程不存在", false, r)
		return
	}
	if err != nil || currentRevision != input.ExpectedRevision {
		writeError(w, http.StatusConflict, "EDITOR_PROJECT_CONFLICT", "请先完成最新工程保存后再启动分层", false, r)
		return
	}
	if err = requireProviderAvailable(r.Context(), tx, model.Provider); err != nil {
		writeError(w, http.StatusServiceUnavailable, "PROVIDER_UNAVAILABLE", "智能分层服务暂不可用，请稍后重试", true, r)
		return
	}
	var active, queued int
	err = tx.QueryRow(r.Context(), `SELECT
		count(*) FILTER(WHERE status NOT IN ('queued','succeeded','failed','cancelled','submission_uncertain')),
		count(*) FILTER(WHERE status='queued') FROM asset_operations WHERE owner_user_id=$1`, currentSession(r).UserID).Scan(&active, &queued)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法确认分层队列状态", true, r)
		return
	}
	if active >= maxActiveEditorOps || queued >= maxQueuedEditorOps {
		writeError(w, http.StatusTooManyRequests, "LAYER_OPERATION_LIMIT", "当前智能分层任务较多，请稍后再试", true, r)
		return
	}
	var operationID uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO asset_operations(
		owner_user_id,editor_project_id,operation_type,source_revision,source_document,model_id,capability_revision,prompt,resolution,
		prompt_optimization_mode,request_hash,idempotency_key,provider_id,provider_model)
		SELECT $1,$2,'layer_decomposition',$3,p.document,$4,$5,$6,$7,$8,$9,$10,$11,$12
		FROM image_editor_projects p WHERE p.id=$2 AND p.owner_user_id=$1 RETURNING id`,
		currentSession(r).UserID, projectID, currentRevision, model.ID, s.catalog.Hash, strings.TrimSpace(input.Prompt),
		input.Resolution, input.PromptOptimizationMode, requestHash, idempotencyKey, model.Provider, model.ProviderModel).Scan(&operationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法创建智能分层任务", true, r)
		return
	}
	payload, _ := json.Marshal(map[string]any{"id": operationID, "status": "queued", "editor_project_id": projectID})
	if _, err = tx.Exec(r.Context(), `INSERT INTO job_events(owner_user_id,asset_operation_id,editor_project_id,event_type,payload)
		VALUES($1,$2,$3,'asset_operation.queued',$4)`, currentSession(r).UserID, operationID, projectID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法创建智能分层任务", true, r)
		return
	}
	_, _ = tx.Exec(r.Context(), `SELECT pg_notify('asset_operations',$1)`, currentSession(r).UserID.String())
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_OPERATION_CREATE_FAILED", "无法创建智能分层任务", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": operationID, "status": "queued", "editor_project_id": projectID, "source_revision": currentRevision})
}

func (s *Server) getAssetOperation(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	operation, err := s.loadAssetOperation(r, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "ASSET_OPERATION_NOT_FOUND", "图片处理任务不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取图片处理任务失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, operation)
}

func (s *Server) loadAssetOperation(r *http.Request, id uuid.UUID) (assetOperationResponse, error) {
	var item assetOperationResponse
	var createdAt, updatedAt time.Time
	var startedAt *time.Time
	err := s.db.QueryRow(r.Context(), `SELECT id,editor_project_id,operation_type,status,source_revision,resolution,prompt_optimization_mode,error_code,error_message,submission_uncertain,result_asset_id,started_at,created_at,updated_at
		FROM asset_operations WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID).Scan(
		&item.ID, &item.ProjectID, &item.Type, &item.Status, &item.SourceRevision, &item.Resolution,
		&item.PromptOptimizationMode, &item.ErrorCode, &item.ErrorMessage, &item.SubmissionUncertain, &item.ResultAssetID, &startedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return item, err
	}
	item.CreatedAt, item.UpdatedAt = editorTime(createdAt), editorTime(updatedAt)
	if startedAt != nil {
		value := editorTime(*startedAt)
		item.StartedAt = &value
	}
	if item.Status == "succeeded" {
		item.LayerSet, err = s.loadLayerSet(r, id, item.ProjectID, item.SourceRevision)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
	}
	return item, err
}

func (s *Server) loadLayerSet(r *http.Request, operationID, projectID uuid.UUID, sourceRevision int64) (*layerSetResponse, error) {
	var response layerSetResponse
	var baseID uuid.UUID
	var packageAssetID *uuid.UUID
	var packageReadyAt *time.Time
	err := s.db.QueryRow(r.Context(), `SELECT id,base_asset_id,package_asset_id,package_ready_at FROM layer_sets
		WHERE asset_operation_id=$1 AND owner_user_id=$2`, operationID, currentSession(r).UserID).Scan(&response.ID, &baseID, &packageAssetID, &packageReadyAt)
	if err != nil {
		return nil, err
	}
	response.SourceRevision = sourceRevision
	response.PackageReady = packageAssetID != nil && packageReadyAt != nil
	var active *uuid.UUID
	_ = s.db.QueryRow(r.Context(), `SELECT active_layer_set_id FROM image_editor_projects WHERE id=$1 AND owner_user_id=$2`, projectID, currentSession(r).UserID).Scan(&active)
	response.AppliedToProject = active != nil && *active == response.ID
	base, _, err := s.loadAsset(r, baseID)
	if err != nil {
		return nil, err
	}
	response.BaseAsset = base
	rows, err := s.db.Query(r.Context(), `SELECT i.id,i.asset_id,i.z_index,i.name,i.description,i.bbox_absolute,i.bbox_normalized
		FROM layer_set_items i WHERE i.layer_set_id=$1 ORDER BY i.z_index`, response.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var layer layerItemResponse
		var assetID uuid.UUID
		var absolute []int32
		if err = rows.Scan(&layer.ID, &assetID, &layer.ZIndex, &layer.Name, &layer.Description, &absolute, &layer.BoundingBoxNormalized); err != nil {
			return nil, err
		}
		layer.BoundingBoxAbsolute = make([]int, len(absolute))
		for index, value := range absolute {
			layer.BoundingBoxAbsolute[index] = int(value)
		}
		layer.Asset, _, err = s.loadAsset(r, assetID)
		if err != nil {
			return nil, err
		}
		response.Items = append(response.Items, layer)
	}
	return &response, rows.Err()
}

// The remaining publish/package endpoints are deliberately explicit. Their
// worker implementations are queued through asset_operations so they share
// revision, idempotency and lifecycle semantics with decomposition.
func (s *Server) publishEditorProject(w http.ResponseWriter, r *http.Request) {
	s.createSimpleEditorOperation(w, r, "editor_publish")
}

func (s *Server) publishLayerItem(w http.ResponseWriter, r *http.Request) {
	layerSetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	itemID, ok := parseUUIDParam(w, r, "itemId")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var source assetResponse
	var storageKey string
	var layerName string
	var publishedAssetID *uuid.UUID
	var createdAt time.Time
	err = tx.QueryRow(r.Context(), `SELECT a.id,a.media_type,a.width,a.height,a.byte_size,a.sha256,a.blur_data_url,a.storage_key,i.name,i.published_asset_id
		FROM layer_set_items i JOIN layer_sets s ON s.id=i.layer_set_id JOIN assets a ON a.id=i.asset_id
		WHERE s.id=$1 AND i.id=$2 AND s.owner_user_id=$3 FOR UPDATE OF i`, layerSetID, itemID, currentSession(r).UserID).Scan(
		&source.ID, &source.MediaType, &source.Width, &source.Height, &source.ByteSize, &source.SHA256, &source.BlurDataURL, &storageKey, &layerName, &publishedAssetID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "LAYER_ITEM_NOT_FOUND", "图层不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
		return
	}
	if publishedAssetID != nil {
		if err = tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
			return
		}
		item, _, loadErr := s.loadAsset(r, *publishedAssetID)
		if loadErr != nil {
			writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "读取已保存图层失败", true, r)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	var item assetResponse
	err = tx.QueryRow(r.Context(), `INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,original_filename,width,height,byte_size,blur_data_url)
		VALUES($1,'editor',$2,$3,$4,$5||'.png',$6,$7,$8,$9)
		RETURNING id,kind,media_type,original_filename,width,height,byte_size,sha256,blur_data_url,created_at`, currentSession(r).UserID, storageKey, source.SHA256, source.MediaType, layerName, source.Width, source.Height, source.ByteSize, source.BlurDataURL).Scan(
		&item.ID, &item.Kind, &item.MediaType, &item.OriginalFilename, &item.Width, &item.Height, &item.ByteSize, &item.SHA256, &item.BlurDataURL, &createdAt,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
		return
	}
	item.CreatedAt = editorTime(createdAt)
	item.setURLs()
	if _, err = tx.Exec(r.Context(), `UPDATE layer_set_items SET published_asset_id=$2 WHERE id=$1`, itemID, item.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_ITEM_PUBLISH_FAILED", "保存图层失败", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) createLayerPackage(w http.ResponseWriter, r *http.Request) {
	layerSetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "需要有效的 Idempotency-Key", false, r)
		return
	}
	var projectID uuid.UUID
	var revision int64
	var document []byte
	var ready bool
	err := s.db.QueryRow(r.Context(), `SELECT s.editor_project_id,s.source_revision,p.document,(s.package_ready_at IS NOT NULL)
		FROM layer_sets s JOIN image_editor_projects p ON p.id=s.editor_project_id
		WHERE s.id=$1 AND s.owner_user_id=$2`, layerSetID, currentSession(r).UserID).Scan(&projectID, &revision, &document, &ready)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "LAYER_SET_NOT_FOUND", "图层结果不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_PACKAGE_FAILED", "无法创建图层压缩包", true, r)
		return
	}
	if ready {
		writeJSON(w, http.StatusOK, map[string]any{"status": "succeeded", "content_url": "/api/v1/layer-sets/" + layerSetID.String() + "/package/content"})
		return
	}
	hash := sha256.Sum256([]byte("layer_package:" + layerSetID.String()))
	requestHash := hex.EncodeToString(hash[:])
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_PACKAGE_FAILED", "无法创建图层压缩包", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var operationID uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO asset_operations(owner_user_id,editor_project_id,operation_type,source_revision,source_document,layer_set_id,request_hash,idempotency_key)
		VALUES($1,$2,'layer_package',$3,$4,$5,$6,$7)
		ON CONFLICT(owner_user_id,idempotency_key) DO NOTHING RETURNING id`, currentSession(r).UserID, projectID, revision, document, layerSetID, requestHash, idempotencyKey).Scan(&operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash string
		err = tx.QueryRow(r.Context(), `SELECT id,request_hash FROM asset_operations WHERE owner_user_id=$1 AND idempotency_key=$2`, currentSession(r).UserID, idempotencyKey).Scan(&operationID, &existingHash)
		if err == nil && existingHash != requestHash {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key 已用于其他操作", false, r)
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_PACKAGE_FAILED", "无法创建图层压缩包", true, r)
		return
	}
	_, _ = tx.Exec(r.Context(), `SELECT pg_notify('asset_operations',$1)`, currentSession(r).UserID.String())
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "LAYER_PACKAGE_FAILED", "无法创建图层压缩包", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": operationID, "status": "queued"})
}

func (s *Server) layerPackageContent(w http.ResponseWriter, r *http.Request) {
	layerSetID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var key string
	err := s.db.QueryRow(r.Context(), `SELECT a.storage_key FROM layer_sets s JOIN assets a ON a.id=s.package_asset_id
		WHERE s.id=$1 AND s.owner_user_id=$2 AND s.package_ready_at IS NOT NULL AND a.purged_at IS NULL`, layerSetID, currentSession(r).UserID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "LAYER_PACKAGE_NOT_READY", "图层压缩包尚未准备完成", true, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取图层压缩包失败", true, r)
		return
	}
	if _, err = s.blobs.Resolve(key); err != nil {
		writeError(w, http.StatusNotFound, "LAYER_PACKAGE_NOT_READY", "图层压缩包尚未准备完成", true, r)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="cornfield-layers.zip"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Redirect", "/_protected_assets/"+key)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) createSimpleEditorOperation(w http.ResponseWriter, r *http.Request, operationType string) {
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", "需要有效的 Idempotency-Key", false, r)
		return
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", operationType, projectID, input.ExpectedRevision)))
	requestHash := hex.EncodeToString(hash[:])
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PUBLISH_FAILED", "无法创建图片发布任务", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var operationID uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO asset_operations(owner_user_id,editor_project_id,operation_type,source_revision,source_document,request_hash,idempotency_key)
		SELECT $1,p.id,$3,$4,p.document,$5,$6 FROM image_editor_projects p WHERE p.id=$2 AND p.owner_user_id=$1 AND p.revision=$4
		ON CONFLICT(owner_user_id,idempotency_key) DO NOTHING RETURNING id`, currentSession(r).UserID, projectID, operationType, input.ExpectedRevision, requestHash, key).Scan(&operationID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingHash string
		err = tx.QueryRow(r.Context(), `SELECT id,request_hash FROM asset_operations WHERE owner_user_id=$1 AND idempotency_key=$2`, currentSession(r).UserID, key).Scan(&operationID, &existingHash)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "EDITOR_PROJECT_CONFLICT", "请先完成最新工程保存后再发布", false, r)
			return
		}
		if err == nil && existingHash != requestHash {
			writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key 已用于其他操作", false, r)
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PUBLISH_FAILED", "无法创建图片发布任务", true, r)
		return
	}
	_, _ = tx.Exec(r.Context(), `SELECT pg_notify('asset_operations',$1)`, currentSession(r).UserID.String())
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "EDITOR_PUBLISH_FAILED", "无法创建图片发布任务", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": operationID, "status": "queued"})
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
