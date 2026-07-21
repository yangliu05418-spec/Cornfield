package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

const maximumBulkAssets = 100

func uniqueBulkAssetIDs(values []uuid.UUID) ([]uuid.UUID, bool) {
	if len(values) < 1 || len(values) > maximumBulkAssets {
		return nil, false
	}
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}

func (s *Server) organizeAssetsBulk(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AssetIDs []uuid.UUID     `json:"asset_ids"`
		FolderID json.RawMessage `json:"folder_id"`
		Archived *bool           `json:"archived"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	assetIDs, ok := uniqueBulkAssetIDs(input.AssetIDs)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "ASSET_IDS_INVALID", "每次请选择 1 到 100 个有效资产", false, r)
		return
	}
	var folderID *uuid.UUID
	setFolder := len(input.FolderID) > 0
	if setFolder && !bytes.Equal(bytes.TrimSpace(input.FolderID), []byte("null")) {
		var parsed uuid.UUID
		if json.Unmarshal(input.FolderID, &parsed) != nil || parsed == uuid.Nil {
			writeError(w, http.StatusUnprocessableEntity, "FOLDER_INVALID", "目标文件夹无效", false, r)
			return
		}
		folderID = &parsed
	}
	if !setFolder && input.Archived == nil {
		writeError(w, http.StatusUnprocessableEntity, "ORGANIZATION_EMPTY", "没有可更新的资产组织字段", false, r)
		return
	}

	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	lockedRows, err := tx.Query(r.Context(), `SELECT id FROM assets WHERE id=ANY($1) AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false FOR UPDATE`, assetIDs, sess.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	owned := 0
	for lockedRows.Next() {
		owned++
	}
	err = lockedRows.Err()
	lockedRows.Close()
	if err != nil || owned != len(assetIDs) {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "部分资产不存在或无权操作", false, r)
		return
	}
	if folderID != nil {
		var exists bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM asset_folders WHERE id=$1 AND owner_user_id=$2)`, *folderID, sess.UserID).Scan(&exists); err != nil || !exists {
			writeError(w, http.StatusUnprocessableEntity, "FOLDER_NOT_FOUND", "目标文件夹不存在", false, r)
			return
		}
	}
	command, err := tx.Exec(r.Context(), `UPDATE assets SET
		folder_id=CASE WHEN $3 THEN $4 ELSE folder_id END,
		archived_at=CASE WHEN $5::boolean IS NULL THEN archived_at WHEN $5 THEN now() ELSE NULL END
		WHERE id=ANY($1) AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false`, assetIDs, sess.UserID, setFolder, folderID, input.Archived)
	if err != nil || command.RowsAffected() != int64(len(assetIDs)) || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": len(assetIDs)})
}

func (s *Server) deleteAssetsBulk(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AssetIDs []uuid.UUID `json:"asset_ids"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	assetIDs, ok := uniqueBulkAssetIDs(input.AssetIDs)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "ASSET_IDS_INVALID", "每次请选择 1 到 100 个有效资产", false, r)
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT id,purge_pending FROM assets WHERE id=ANY($1) AND owner_user_id=$2 AND purged_at IS NULL FOR UPDATE`, assetIDs, sess.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	pending := make(map[uuid.UUID]bool, len(assetIDs))
	for rows.Next() {
		var id uuid.UUID
		var isPending bool
		if err = rows.Scan(&id, &isPending); err != nil {
			break
		}
		pending[id] = isPending
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil || len(pending) != len(assetIDs) {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "部分资产不存在或无权操作", false, r)
		return
	}
	deletionIDs := make([]uuid.UUID, 0, len(assetIDs))
	for _, assetID := range assetIDs {
		var deletionID uuid.UUID
		if pending[assetID] {
			err = tx.QueryRow(r.Context(), `SELECT id FROM deletion_requests WHERE asset_id=$1 AND status IN ('pending','running')`, assetID).Scan(&deletionID)
		}
		if !pending[assetID] || isNotFound(err) {
			if _, err = tx.Exec(r.Context(), `UPDATE assets SET purge_pending=true WHERE id=$1`, assetID); err == nil {
				_, err = tx.Exec(r.Context(), `UPDATE generation_outputs SET deleted_at=COALESCE(deleted_at,now()) WHERE asset_id=$1`, assetID)
			}
			if err == nil {
				err = tx.QueryRow(r.Context(), `INSERT INTO deletion_requests(kind,owner_user_id,asset_id,requested_by) VALUES('asset',$1,$2,$1) RETURNING id`, sess.UserID, assetID).Scan(&deletionID)
			}
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
			return
		}
		deletionIDs = append(deletionIDs, deletionID)
	}
	if err = insertAudit(r.Context(), tx, sess.UserID, "asset.bulk_delete_requested", "asset", "bulk", requestIDFromContext(r), map[string]any{"asset_ids": assetIDs, "deletion_ids": deletionIDs}); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_DELETE_FAILED", "删除资产失败", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"items": deletionIDs, "status": "pending"})
}
