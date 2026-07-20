package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func validFolderName(name string) bool {
	length := utf8.RuneCountInString(name)
	return utf8.ValidString(name) && length >= 1 && length <= 64
}

func (s *Server) listAssetFolders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT f.id,f.name,count(a.id)::int,f.created_at
		FROM asset_folders f LEFT JOIN assets a ON a.folder_id=f.id AND a.purged_at IS NULL AND a.purge_pending=false
		WHERE f.owner_user_id=$1 GROUP BY f.id ORDER BY lower(f.name),f.id`, currentSession(r).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取文件夹失败", true, r)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var name string
		var count int
		var created time.Time
		if err = rows.Scan(&id, &name, &count, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取文件夹失败", true, r)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "asset_count": count, "created_at": created})
	}
	if err = rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取文件夹失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAssetFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validFolderName(input.Name) {
		writeError(w, http.StatusUnprocessableEntity, "FOLDER_NAME_INVALID", "文件夹名称需为 1–64 个字符", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_CREATE_FAILED", "创建文件夹失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::text,7123))`, currentSession(r).UserID.String()); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_CREATE_FAILED", "创建文件夹失败", true, r)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM asset_folders WHERE owner_user_id=$1`, currentSession(r).UserID).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_CREATE_FAILED", "创建文件夹失败", true, r)
		return
	}
	if count >= 100 {
		writeError(w, http.StatusConflict, "FOLDER_LIMIT", "每名用户最多创建 100 个文件夹", false, r)
		return
	}
	var id uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO asset_folders(owner_user_id,name) VALUES($1,$2) RETURNING id`, currentSession(r).UserID, input.Name).Scan(&id)
	if err != nil {
		writeFolderConflict(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_CREATE_FAILED", "创建文件夹失败", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": input.Name, "asset_count": 0})
}

func (s *Server) updateAssetFolder(w http.ResponseWriter, r *http.Request) {
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
	if !validFolderName(input.Name) {
		writeError(w, http.StatusUnprocessableEntity, "FOLDER_NAME_INVALID", "文件夹名称需为 1–64 个字符", false, r)
		return
	}
	command, err := s.db.Exec(r.Context(), `UPDATE asset_folders SET name=$3,updated_at=now() WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID, input.Name)
	if err != nil {
		writeFolderConflict(w, r, err)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FOLDER_NOT_FOUND", "文件夹不存在", false, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAssetFolder(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_DELETE_FAILED", "删除文件夹失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var owned bool
	if err = tx.QueryRow(r.Context(), `SELECT true FROM asset_folders WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, currentSession(r).UserID).Scan(&owned); err != nil || !owned {
		writeError(w, http.StatusNotFound, "FOLDER_NOT_FOUND", "文件夹不存在", false, r)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE assets SET folder_id=NULL,archived_at=NULL WHERE folder_id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_DELETE_FAILED", "删除文件夹失败", true, r)
		return
	}
	command, err := tx.Exec(r.Context(), `DELETE FROM asset_folders WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "FOLDER_NOT_FOUND", "文件夹不存在", false, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "FOLDER_DELETE_FAILED", "删除文件夹失败", true, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) organizeAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		FolderID json.RawMessage `json:"folder_id"`
		Archived *bool           `json:"archived"`
	}
	if !decodeJSON(w, r, &input) {
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
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	if folderID != nil {
		var exists bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM asset_folders WHERE id=$1 AND owner_user_id=$2)`, *folderID, currentSession(r).UserID).Scan(&exists); err != nil || !exists {
			writeError(w, http.StatusUnprocessableEntity, "FOLDER_NOT_FOUND", "目标文件夹不存在", false, r)
			return
		}
	}
	command, err := tx.Exec(r.Context(), `UPDATE assets SET
		folder_id=CASE WHEN $3 THEN $4 ELSE folder_id END,
		archived_at=CASE WHEN $5::boolean IS NULL THEN archived_at WHEN $5 THEN now() ELSE NULL END
		WHERE id=$1 AND owner_user_id=$2 AND purged_at IS NULL AND purge_pending=false`, id, currentSession(r).UserID, setFolder, folderID, input.Archived)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "资产不存在", false, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UPDATE_FAILED", "更新资产失败", true, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFolderConflict(w http.ResponseWriter, r *http.Request, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, http.StatusConflict, "FOLDER_NAME_EXISTS", "已有同名文件夹", false, r)
		return
	}
	writeError(w, http.StatusInternalServerError, "FOLDER_WRITE_FAILED", "保存文件夹失败", true, r)
}
