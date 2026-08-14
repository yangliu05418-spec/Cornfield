package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	studioEditor "internal-image-studio/internal/editor"
)

const (
	rasterMaskTileSize       = 256
	maxRasterMaskChanges     = 576
	maxRasterMaskManifest    = 128 << 10
	maxRasterMaskCommitBytes = 40 << 20
)

type rasterMaskChange struct {
	TileX  int    `json:"tile_x"`
	TileY  int    `json:"tile_y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Action string `json:"action"`
	Part   string `json:"part,omitempty"`
}

type rasterMaskCommitManifest struct {
	ExpectedProjectRevision int64              `json:"expected_project_revision"`
	ExpectedMaskVersion     int64              `json:"expected_mask_version"`
	Changes                 []rasterMaskChange `json:"changes"`
}

type storedRasterMaskTile struct {
	rasterMaskChange
	StorageKey string
	SHA256     string
	ByteSize   int64
	LeaseID    uuid.UUID
}

type rasterMaskResponse struct {
	ID              uuid.UUID `json:"id"`
	ProjectID       uuid.UUID `json:"editor_project_id"`
	TargetNodeID    string    `json:"target_node_id"`
	Width           int       `json:"width"`
	Height          int       `json:"height"`
	DefaultAlpha    int       `json:"default_alpha"`
	CurrentVersion  int64     `json:"current_version"`
	ProjectRevision int64     `json:"project_revision"`
}

func (s *Server) createEditorRasterMask(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64  `json:"expected_revision"`
		TargetNodeID     string `json:"target_node_id"`
		DefaultAlpha     int    `json:"default_alpha"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 0 || input.DefaultAlpha < 0 || input.DefaultAlpha > 255 || len(input.TargetNodeID) < 1 || len(input.TargetNodeID) > 64 || strings.TrimSpace(input.TargetNodeID) != input.TargetNodeID {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_RASTER_MASK", "蒙版参数无效", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法创建像素蒙版", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var raw json.RawMessage
	var revision int64
	err = tx.QueryRow(r.Context(), `SELECT document,revision FROM image_editor_projects
		WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, projectID, currentSession(r).UserID).Scan(&raw, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "EDITOR_PROJECT_NOT_FOUND", "图片编辑工程不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法创建像素蒙版", true, r)
		return
	}
	document, node, err := editorRasterNode(raw, input.TargetNodeID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "RASTER_MASK_TARGET_INVALID", "当前图层不能添加像素蒙版", false, r)
		return
	}
	if node.PixelMask != nil {
		var response rasterMaskResponse
		err = tx.QueryRow(r.Context(), `SELECT id,editor_project_id,target_node_id,width,height,default_alpha,current_version,$2
			FROM editor_raster_masks WHERE id=$1 AND owner_user_id=$3`, node.PixelMask.ResourceID, revision, currentSession(r).UserID).Scan(
			&response.ID, &response.ProjectID, &response.TargetNodeID, &response.Width, &response.Height,
			&response.DefaultAlpha, &response.CurrentVersion, &response.ProjectRevision,
		)
		if err != nil {
			writeError(w, http.StatusConflict, "RASTER_MASK_REFERENCE_INVALID", "工程中的像素蒙版引用已失效", false, r)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if revision != input.ExpectedRevision {
		writeError(w, http.StatusConflict, "EDITOR_PROJECT_CONFLICT", "工程已在其他页面更新，请刷新后继续", false, r)
		return
	}
	var width, height int
	err = tx.QueryRow(r.Context(), `SELECT width,height FROM assets
		WHERE id=$1 AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false FOR KEY SHARE`, *node.AssetID, currentSession(r).UserID).Scan(&width, &height)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "EDITOR_ASSET_INVALID", "图层图片不存在或不可访问", false, r)
		return
	}
	if int64(width)*int64(height) > studioEditor.MaxCanvasPixels {
		writeError(w, http.StatusUnprocessableEntity, "RASTER_MASK_SOURCE_TOO_LARGE", "图片尺寸超过像素蒙版的 36MP 上限", false, r)
		return
	}
	maskID := uuid.New()
	nextRevision := revision + 1
	if _, err = tx.Exec(r.Context(), `INSERT INTO editor_raster_masks
		(id,owner_user_id,editor_project_id,target_node_id,source_asset_id,width,height,default_alpha)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, maskID, currentSession(r).UserID, projectID, input.TargetNodeID, *node.AssetID, width, height, input.DefaultAlpha); err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法创建像素蒙版", true, r)
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO editor_raster_mask_versions(mask_id,version,base_version,project_revision)
		VALUES($1,0,NULL,$2)`, maskID, nextRevision); err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法创建像素蒙版", true, r)
		return
	}
	node.PixelMask = &studioEditor.PixelMaskV2{ResourceID: maskID, Version: 0}
	updatedDocument, err := json.Marshal(document)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法保存像素蒙版", true, r)
		return
	}
	command, err := tx.Exec(r.Context(), `UPDATE image_editor_projects SET document=$3,revision=$4,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$5`, projectID, currentSession(r).UserID, updatedDocument, nextRevision, revision)
	if err != nil || command.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "EDITOR_PROJECT_CONFLICT", "工程已在其他页面更新，请刷新后继续", false, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_CREATE_FAILED", "无法创建像素蒙版", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, rasterMaskResponse{
		ID: maskID, ProjectID: projectID, TargetNodeID: input.TargetNodeID,
		Width: width, Height: height, DefaultAlpha: input.DefaultAlpha,
		CurrentVersion: 0, ProjectRevision: nextRevision,
	})
}

func (s *Server) commitEditorRasterMask(w http.ResponseWriter, r *http.Request) {
	select {
	case s.rasterMaskWrites <- struct{}{}:
		defer func() { <-s.rasterMaskWrites }()
	default:
		writeError(w, http.StatusTooManyRequests, "RASTER_MASK_CAPACITY", "像素蒙版保存任务较多，请稍后重试", true, r)
		return
	}
	free, err := storageFreePercent(s.cfg.AssetRoot)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "存储状态无法确认，已暂停像素蒙版保存", true, r)
		return
	}
	if free < 15 {
		writeError(w, http.StatusServiceUnavailable, "DISK_PRESSURE", "存储空间不足，已暂停像素蒙版保存", true, r)
		return
	}
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	maskID, err := uuid.Parse(r.PathValue("maskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_RASTER_MASK_ID", "像素蒙版 ID 无效", false, r)
		return
	}
	var width, height, defaultAlpha int
	var currentVersion, projectRevision int64
	err = s.db.QueryRow(r.Context(), `SELECT m.width,m.height,m.default_alpha,m.current_version,p.revision
		FROM editor_raster_masks m JOIN image_editor_projects p ON p.id=m.editor_project_id
		WHERE m.id=$1 AND m.editor_project_id=$2 AND m.owner_user_id=$3`, maskID, projectID, currentSession(r).UserID).Scan(
		&width, &height, &defaultAlpha, &currentVersion, &projectRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "RASTER_MASK_NOT_FOUND", "像素蒙版不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_COMMIT_FAILED", "无法保存像素蒙版", true, r)
		return
	}
	manifest, reader, err := readRasterMaskManifest(w, r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "RASTER_MASK_COMMIT_INVALID", err.Error(), false, r)
		return
	}
	if manifest.ExpectedMaskVersion < 0 || manifest.ExpectedMaskVersion > currentVersion || manifest.ExpectedProjectRevision != projectRevision {
		writeError(w, http.StatusConflict, "RASTER_MASK_CONFLICT", "像素蒙版或工程已在其他页面更新", false, r)
		return
	}
	if err = validateRasterMaskChanges(manifest.Changes, width, height); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "RASTER_MASK_COMMIT_INVALID", err.Error(), false, r)
		return
	}
	stored, err := s.receiveRasterMaskTiles(r, reader, manifest.Changes, defaultAlpha)
	if err != nil {
		s.releaseBlobWriteLeases(stored)
		if errors.Is(err, errRasterMaskStorage) {
			writeError(w, http.StatusInternalServerError, "RASTER_MASK_STORAGE_FAILED", "无法保存像素蒙版，请稍后重试", true, r)
			return
		}
		writeError(w, http.StatusUnprocessableEntity, "RASTER_MASK_TILE_INVALID", err.Error(), false, r)
		return
	}
	defer s.releaseBlobWriteLeases(stored)
	response, err := s.commitRasterMaskVersion(r, projectID, maskID, manifest, stored)
	if errors.Is(err, errRasterMaskConflict) {
		writeError(w, http.StatusConflict, "RASTER_MASK_CONFLICT", "像素蒙版或工程已在其他页面更新", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "RASTER_MASK_COMMIT_FAILED", "无法保存像素蒙版", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

var errRasterMaskConflict = errors.New("raster mask revision conflict")
var errRasterMaskStorage = errors.New("raster mask storage failure")

func (s *Server) commitRasterMaskVersion(r *http.Request, projectID, maskID uuid.UUID, manifest rasterMaskCommitManifest, stored []storedRasterMaskTile) (rasterMaskResponse, error) {
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		return rasterMaskResponse{}, err
	}
	defer tx.Rollback(r.Context())
	var raw json.RawMessage
	var projectRevision, currentVersion int64
	var targetNodeID string
	var sourceAssetID uuid.UUID
	var width, height, defaultAlpha int
	err = tx.QueryRow(r.Context(), `SELECT p.document,p.revision,m.current_version,m.target_node_id,m.source_asset_id,m.width,m.height,m.default_alpha
		FROM image_editor_projects p JOIN editor_raster_masks m ON m.editor_project_id=p.id
		WHERE p.id=$1 AND p.owner_user_id=$2 AND m.id=$3 FOR UPDATE OF p,m`, projectID, currentSession(r).UserID, maskID).Scan(
		&raw, &projectRevision, &currentVersion, &targetNodeID, &sourceAssetID, &width, &height, &defaultAlpha,
	)
	if err != nil {
		return rasterMaskResponse{}, err
	}
	if projectRevision != manifest.ExpectedProjectRevision || currentVersion < manifest.ExpectedMaskVersion {
		return rasterMaskResponse{}, errRasterMaskConflict
	}
	document, node, err := editorRasterNode(raw, targetNodeID)
	if err != nil || node.AssetID == nil || *node.AssetID != sourceAssetID || node.PixelMask == nil || node.PixelMask.ResourceID != maskID || node.PixelMask.Version != manifest.ExpectedMaskVersion {
		return rasterMaskResponse{}, errRasterMaskConflict
	}
	nextVersion, nextProjectRevision := currentVersion+1, projectRevision+1
	if _, err = tx.Exec(r.Context(), `INSERT INTO editor_raster_mask_versions(mask_id,version,base_version,project_revision)
		VALUES($1,$2,$3,$4)`, maskID, nextVersion, manifest.ExpectedMaskVersion, nextProjectRevision); err != nil {
		return rasterMaskResponse{}, err
	}
	changed := make([]string, 0, len(manifest.Changes))
	for _, change := range manifest.Changes {
		changed = append(changed, fmt.Sprintf("%d:%d", change.TileX, change.TileY))
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO editor_raster_mask_version_tiles
		(mask_id,version,tile_x,tile_y,width,height,storage_key,sha256,byte_size)
		SELECT mask_id,$3,tile_x,tile_y,width,height,storage_key,sha256,byte_size
		FROM editor_raster_mask_version_tiles
		WHERE mask_id=$1 AND version=$2 AND (tile_x::text||':'||tile_y::text)<>ALL($4::text[])`, maskID, manifest.ExpectedMaskVersion, nextVersion, changed); err != nil {
		return rasterMaskResponse{}, err
	}
	for _, tile := range stored {
		if _, err = tx.Exec(r.Context(), `INSERT INTO editor_raster_mask_version_tiles
			(mask_id,version,tile_x,tile_y,width,height,storage_key,sha256,byte_size)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, maskID, nextVersion, tile.TileX, tile.TileY, tile.Width, tile.Height, tile.StorageKey, tile.SHA256, tile.ByteSize); err != nil {
			return rasterMaskResponse{}, err
		}
	}
	node.PixelMask.Version = nextVersion
	updatedDocument, err := json.Marshal(document)
	if err != nil {
		return rasterMaskResponse{}, err
	}
	command, err := tx.Exec(r.Context(), `UPDATE editor_raster_masks SET current_version=$2,updated_at=now()
		WHERE id=$1 AND current_version=$3`, maskID, nextVersion, currentVersion)
	if err != nil || command.RowsAffected() != 1 {
		return rasterMaskResponse{}, errRasterMaskConflict
	}
	command, err = tx.Exec(r.Context(), `UPDATE image_editor_projects SET document=$3,revision=$4,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$5`, projectID, currentSession(r).UserID, updatedDocument, nextProjectRevision, projectRevision)
	if err != nil || command.RowsAffected() != 1 {
		return rasterMaskResponse{}, errRasterMaskConflict
	}
	if err = tx.Commit(r.Context()); err != nil {
		return rasterMaskResponse{}, err
	}
	return rasterMaskResponse{
		ID: maskID, ProjectID: projectID, TargetNodeID: targetNodeID,
		Width: width, Height: height, DefaultAlpha: defaultAlpha,
		CurrentVersion: nextVersion, ProjectRevision: nextProjectRevision,
	}, nil
}

func (s *Server) getEditorRasterMaskVersion(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	maskID, err := uuid.Parse(r.PathValue("maskID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_RASTER_MASK_ID", "像素蒙版 ID 无效", false, r)
		return
	}
	version, err := strconv.ParseInt(r.PathValue("version"), 10, 64)
	if err != nil || version < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_RASTER_MASK_VERSION", "像素蒙版版本无效", false, r)
		return
	}
	var response rasterMaskResponse
	err = s.db.QueryRow(r.Context(), `SELECT m.id,m.editor_project_id,m.target_node_id,m.width,m.height,m.default_alpha,m.current_version,p.revision
		FROM editor_raster_masks m JOIN image_editor_projects p ON p.id=m.editor_project_id
		JOIN editor_raster_mask_versions v ON v.mask_id=m.id AND v.version=$4
		WHERE m.id=$1 AND m.editor_project_id=$2 AND m.owner_user_id=$3`, maskID, projectID, currentSession(r).UserID, version).Scan(
		&response.ID, &response.ProjectID, &response.TargetNodeID, &response.Width, &response.Height,
		&response.DefaultAlpha, &response.CurrentVersion, &response.ProjectRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "RASTER_MASK_VERSION_NOT_FOUND", "像素蒙版版本不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取像素蒙版失败", true, r)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT tile_x,tile_y,width,height,sha256,byte_size
		FROM editor_raster_mask_version_tiles WHERE mask_id=$1 AND version=$2 ORDER BY tile_y,tile_x`, maskID, version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取像素蒙版失败", true, r)
		return
	}
	defer rows.Close()
	tiles := make([]map[string]any, 0)
	for rows.Next() {
		var x, y, width, height, size int
		var digest string
		if err = rows.Scan(&x, &y, &width, &height, &digest, &size); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取像素蒙版失败", true, r)
			return
		}
		tiles = append(tiles, map[string]any{
			"tile_x": x, "tile_y": y, "width": width, "height": height,
			"sha256": digest, "byte_size": size,
			"url": fmt.Sprintf("/api/v1/editor-projects/%s/raster-masks/%s/versions/%d/tiles/%d/%d/content", projectID, maskID, version, x, y),
		})
	}
	if err = rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取像素蒙版失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mask": response, "version": version, "tiles": tiles})
}

func (s *Server) editorRasterMaskTileContent(w http.ResponseWriter, r *http.Request) {
	projectID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	maskID, err := uuid.Parse(r.PathValue("maskID"))
	version, versionErr := strconv.ParseInt(r.PathValue("version"), 10, 64)
	tileX, xErr := strconv.Atoi(r.PathValue("tileX"))
	tileY, yErr := strconv.Atoi(r.PathValue("tileY"))
	if err != nil || versionErr != nil || xErr != nil || yErr != nil || version < 0 || tileX < 0 || tileY < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_RASTER_MASK_TILE", "像素蒙版 Tile 参数无效", false, r)
		return
	}
	var key, digest string
	var size int
	err = s.db.QueryRow(r.Context(), `SELECT t.storage_key,t.sha256,t.byte_size
		FROM editor_raster_mask_version_tiles t JOIN editor_raster_masks m ON m.id=t.mask_id
		WHERE t.mask_id=$1 AND t.version=$2 AND t.tile_x=$3 AND t.tile_y=$4
		AND m.editor_project_id=$5 AND m.owner_user_id=$6`, maskID, version, tileX, tileY, projectID, currentSession(r).UserID).Scan(&key, &digest, &size)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "RASTER_MASK_TILE_NOT_FOUND", "像素蒙版 Tile 不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取像素蒙版失败", true, r)
		return
	}
	etag := `"` + digest + `"`
	w.Header().Set("Content-Type", "application/vnd.cornfield.alpha8")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("X-Accel-Redirect", "/_protected_assets/"+key)
	w.WriteHeader(http.StatusOK)
}

func editorRasterNode(raw json.RawMessage, nodeID string) (*studioEditor.DocumentV2, *studioEditor.NodeV2, error) {
	decoded, err := studioEditor.DecodeAny(raw)
	if err != nil || decoded.SchemaVersion != 2 || decoded.V2 == nil {
		return nil, nil, studioEditor.ErrInvalidDocument
	}
	for index := range decoded.V2.Nodes {
		node := &decoded.V2.Nodes[index]
		if node.ID == nodeID && node.Type == "raster" && node.AssetID != nil {
			return decoded.V2, node, nil
		}
	}
	return nil, nil, studioEditor.ErrInvalidDocument
}

func readRasterMaskManifest(w http.ResponseWriter, r *http.Request) (rasterMaskCommitManifest, *multipart.Reader, error) {
	var manifest rasterMaskCommitManifest
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return manifest, nil, errors.New("像素蒙版提交必须使用 multipart/form-data")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRasterMaskCommitBytes)
	reader := multipart.NewReader(r.Body, params["boundary"])
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "manifest" {
		return manifest, nil, errors.New("像素蒙版提交缺少 manifest")
	}
	data, err := io.ReadAll(io.LimitReader(part, maxRasterMaskManifest+1))
	if err != nil || len(data) > maxRasterMaskManifest {
		return manifest, nil, errors.New("像素蒙版 manifest 超过大小限制")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&manifest); err != nil {
		return manifest, nil, errors.New("像素蒙版 manifest 无效")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return manifest, nil, errors.New("像素蒙版 manifest 无效")
	}
	return manifest, reader, nil
}

func validateRasterMaskChanges(changes []rasterMaskChange, width, height int) error {
	if len(changes) < 1 || len(changes) > maxRasterMaskChanges {
		return errors.New("每次需要提交 1 到 576 个变化 Tile")
	}
	seen := make(map[string]struct{}, len(changes))
	parts := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		key := fmt.Sprintf("%d:%d", change.TileX, change.TileY)
		if _, exists := seen[key]; exists {
			return errors.New("像素蒙版包含重复 Tile")
		}
		seen[key] = struct{}{}
		left, top := change.TileX*rasterMaskTileSize, change.TileY*rasterMaskTileSize
		expectedWidth := min(rasterMaskTileSize, width-left)
		expectedHeight := min(rasterMaskTileSize, height-top)
		if change.TileX < 0 || change.TileY < 0 || expectedWidth < 1 || expectedHeight < 1 || change.Width != expectedWidth || change.Height != expectedHeight {
			return errors.New("像素蒙版 Tile 尺寸或坐标无效")
		}
		switch change.Action {
		case "put":
			if change.Part == "" {
				return errors.New("像素蒙版 Tile 缺少二进制 part")
			}
			if _, exists := parts[change.Part]; exists {
				return errors.New("像素蒙版包含重复二进制 part")
			}
			parts[change.Part] = struct{}{}
		case "delete":
			if change.Part != "" {
				return errors.New("删除 Tile 不能携带二进制 part")
			}
		default:
			return errors.New("像素蒙版 Tile 操作无效")
		}
	}
	return nil
}

func (s *Server) receiveRasterMaskTiles(r *http.Request, reader *multipart.Reader, changes []rasterMaskChange, defaultAlpha int) ([]storedRasterMaskTile, error) {
	byPart := make(map[string]rasterMaskChange)
	for _, change := range changes {
		if change.Action == "put" {
			byPart[change.Part] = change
		}
	}
	stored := make([]storedRasterMaskTile, 0, len(byPart))
	seen := make(map[string]struct{}, len(byPart))
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stored, errors.New("读取像素蒙版 Tile 失败")
		}
		name := part.FormName()
		change, exists := byPart[name]
		if !exists {
			return stored, errors.New("像素蒙版包含未声明的二进制 part")
		}
		if _, duplicate := seen[name]; duplicate {
			return stored, errors.New("像素蒙版包含重复二进制 part")
		}
		seen[name] = struct{}{}
		expected := int64(change.Width * change.Height)
		data, readErr := io.ReadAll(io.LimitReader(part, expected+1))
		if readErr != nil || int64(len(data)) != expected {
			return stored, errors.New("像素蒙版 Tile 字节数无效")
		}
		nonDefault := false
		for _, value := range data {
			if int(value) != defaultAlpha {
				nonDefault = true
				break
			}
		}
		if !nonDefault {
			return stored, errors.New("默认 Tile 应使用 delete 操作")
		}
		tempPath := filepath.Join(s.cfg.AssetRoot, "uploads", "tmp", "raster-mask-"+uuid.NewString()+".part")
		file, openErr := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if openErr != nil {
			return stored, fmt.Errorf("%w: create tile temp file", errRasterMaskStorage)
		}
		_, writeErr := file.Write(data)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		if closeErr := file.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = os.Remove(tempPath)
			return stored, fmt.Errorf("%w: write tile temp file", errRasterMaskStorage)
		}
		digestBytes := sha256.Sum256(data)
		digest := hex.EncodeToString(digestBytes[:])
		key := rasterMaskStorageKey(digest)
		leaseID := uuid.New()
		if _, err = s.db.Exec(r.Context(), `INSERT INTO blob_write_leases(id,owner_user_id,sha256,storage_key,expires_at)
			VALUES($1,$2,$3,$4,now()+interval '5 minutes')`, leaseID, currentSession(r).UserID, digest, key); err != nil {
			_ = os.Remove(tempPath)
			return stored, fmt.Errorf("%w: create blob write lease", errRasterMaskStorage)
		}
		storedKey, storedDigest, size, putErr := s.blobs.PutImmutable(tempPath, "a8")
		_ = os.Remove(tempPath)
		if putErr != nil || storedKey != key || storedDigest != digest || size != expected {
			_, _ = s.db.Exec(r.Context(), `DELETE FROM blob_write_leases WHERE id=$1`, leaseID)
			return stored, fmt.Errorf("%w: commit immutable tile", errRasterMaskStorage)
		}
		stored = append(stored, storedRasterMaskTile{
			rasterMaskChange: change, StorageKey: key, SHA256: digest,
			ByteSize: size, LeaseID: leaseID,
		})
	}
	if len(seen) != len(byPart) {
		return stored, errors.New("像素蒙版缺少已声明的二进制 part")
	}
	return stored, nil
}

func (s *Server) releaseBlobWriteLeases(stored []storedRasterMaskTile) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, tile := range stored {
		if _, err := s.db.Exec(ctx, `DELETE FROM blob_write_leases WHERE id=$1`, tile.LeaseID); err != nil {
			s.log.Warn("blob write lease cleanup failed", "lease_id", tile.LeaseID, "error", err)
		}
	}
}

func rasterMaskStorageKey(digest string) string {
	return filepath.ToSlash(filepath.Join(digest[:2], digest[2:4], digest, "original.a8"))
}
