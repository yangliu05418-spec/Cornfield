package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	maxDirectorProjects      = 100
	maxDirectorDocumentBytes = 4 << 20
)

type directorProjectResponse struct {
	ID        uuid.UUID       `json:"id"`
	Name      string          `json:"name"`
	Document  json.RawMessage `json:"document,omitempty"`
	Revision  int64           `json:"revision"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

func validDirectorProjectName(name string) bool {
	length := utf8.RuneCountInString(name)
	return utf8.ValidString(name) && length >= 1 && length <= 64
}

func decodeDirectorDocument(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDirectorDocumentBytes+4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "DIRECTOR_DOCUMENT_TOO_LARGE", "导演台工程不能超过 4 MiB", false, r)
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_DIRECTOR_JSON", "导演台工程不是有效的 JSON 对象", false, r)
		}
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_DIRECTOR_DOCUMENT", "请求只能包含一个 JSON 对象", false, r)
		return false
	}
	return true
}

func directorProjectTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func (s *Server) listDirectorProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT id,name,revision,created_at,updated_at
		FROM director_projects WHERE owner_user_id=$1 ORDER BY updated_at DESC,id DESC`, currentSession(r).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取导演台项目失败", true, r)
		return
	}
	defer rows.Close()
	items := make([]directorProjectResponse, 0)
	for rows.Next() {
		var item directorProjectResponse
		var createdAt, updatedAt time.Time
		if err = rows.Scan(&item.ID, &item.Name, &item.Revision, &createdAt, &updatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取导演台项目失败", true, r)
			return
		}
		item.CreatedAt = directorProjectTime(createdAt)
		item.UpdatedAt = directorProjectTime(updatedAt)
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取导演台项目失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createDirectorProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validDirectorProjectName(input.Name) {
		writeError(w, http.StatusUnprocessableEntity, "DIRECTOR_PROJECT_NAME_INVALID", "项目名称需为 1–64 个字符", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1::text,8127))`, currentSession(r).UserID.String()); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	var count int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM director_projects WHERE owner_user_id=$1`, currentSession(r).UserID).Scan(&count); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	if count >= maxDirectorProjects {
		writeError(w, http.StatusConflict, "DIRECTOR_PROJECT_LIMIT", "每名用户最多创建 100 个导演台项目", false, r)
		return
	}
	var item directorProjectResponse
	var createdAt, updatedAt time.Time
	err = tx.QueryRow(r.Context(), `INSERT INTO director_projects(owner_user_id,name) VALUES($1,$2)
		RETURNING id,name,revision,created_at,updated_at`, currentSession(r).UserID, input.Name).
		Scan(&item.ID, &item.Name, &item.Revision, &createdAt, &updatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	item.CreatedAt, item.UpdatedAt = directorProjectTime(createdAt), directorProjectTime(updatedAt)
	if err = insertAudit(r.Context(), tx, currentSession(r).UserID, "director_project.created", "director_project", item.ID.String(), requestIDFromContext(r), map[string]any{}); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_CREATE_FAILED", "创建导演台项目失败", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) getDirectorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var item directorProjectResponse
	var document []byte
	var createdAt, updatedAt time.Time
	err := s.db.QueryRow(r.Context(), `SELECT id,name,COALESCE(document,'null'::jsonb),revision,created_at,updated_at
		FROM director_projects WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID).
		Scan(&item.ID, &item.Name, &document, &item.Revision, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "DIRECTOR_PROJECT_NOT_FOUND", "导演台项目不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取导演台项目失败", true, r)
		return
	}
	item.Document = document
	item.CreatedAt, item.UpdatedAt = directorProjectTime(createdAt), directorProjectTime(updatedAt)
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) renameDirectorProject(w http.ResponseWriter, r *http.Request) {
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
	if !validDirectorProjectName(input.Name) {
		writeError(w, http.StatusUnprocessableEntity, "DIRECTOR_PROJECT_NAME_INVALID", "项目名称需为 1–64 个字符", false, r)
		return
	}
	command, err := s.db.Exec(r.Context(), `UPDATE director_projects SET name=$3,updated_at=now() WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID, input.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_UPDATE_FAILED", "更新项目名称失败", true, r)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "DIRECTOR_PROJECT_NOT_FOUND", "导演台项目不存在", false, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) saveDirectorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Document         json.RawMessage `json:"document"`
		ExpectedRevision int64           `json:"expected_revision"`
	}
	if !decodeDirectorDocument(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 0 {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DIRECTOR_REVISION", "工程版本号无效", false, r)
		return
	}
	if err := validateDirectorDocument(input.Document, currentSession(r).UserID); err != nil {
		code, message := directorDocumentValidationMessage(err)
		writeError(w, http.StatusUnprocessableEntity, code, message, false, r)
		return
	}
	var revision int64
	err := s.db.QueryRow(r.Context(), `UPDATE director_projects SET document=$4,revision=revision+1,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$3 RETURNING revision`, id, currentSession(r).UserID, input.ExpectedRevision, input.Document).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if checkErr := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM director_projects WHERE id=$1 AND owner_user_id=$2)`, id, currentSession(r).UserID).Scan(&exists); checkErr != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "保存导演台工程失败", true, r)
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "DIRECTOR_PROJECT_NOT_FOUND", "导演台项目不存在", false, r)
			return
		}
		writeError(w, http.StatusConflict, "DIRECTOR_PROJECT_CONFLICT", "项目已在其他位置更新，请刷新后继续", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_SAVE_FAILED", "保存导演台工程失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func directorDocumentValidationMessage(err error) (string, string) {
	switch {
	case errors.Is(err, errDirectorDocumentTooLarge):
		return "DIRECTOR_DOCUMENT_TOO_LARGE", "导演台工程不能超过 4 MiB"
	case errors.Is(err, errInvalidDirectorJSON):
		return "INVALID_DIRECTOR_JSON", "导演台工程不是有效的 JSON 对象"
	case errors.Is(err, errInvalidDirectorAssetURL):
		return "INVALID_DIRECTOR_ASSET_URL", "工程包含不受支持的模型路径，请移除该模型后重试"
	default:
		return "INVALID_DIRECTOR_DOCUMENT", "导演台工程结构无效，请下载工程文件后重试"
	}
}

func (s *Server) resetDirectorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.ExpectedRevision < 0 {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_DIRECTOR_REVISION", "工程版本号无效", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_RESET_FAILED", "重置导演台工程失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var revision int64
	err = tx.QueryRow(r.Context(), `UPDATE director_projects SET document=NULL,revision=revision+1,updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND revision=$3 RETURNING revision`, id, currentSession(r).UserID, input.ExpectedRevision).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if checkErr := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM director_projects WHERE id=$1 AND owner_user_id=$2)`, id, currentSession(r).UserID).Scan(&exists); checkErr != nil {
			writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_RESET_FAILED", "重置导演台工程失败", true, r)
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "DIRECTOR_PROJECT_NOT_FOUND", "导演台项目不存在", false, r)
			return
		}
		writeError(w, http.StatusConflict, "DIRECTOR_PROJECT_CONFLICT", "项目已在其他位置更新，请刷新后继续", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_RESET_FAILED", "重置导演台工程失败", true, r)
		return
	}
	if err = insertAudit(r.Context(), tx, currentSession(r).UserID, "director_project.reset", "director_project", id.String(), requestIDFromContext(r), map[string]any{"previous_revision": input.ExpectedRevision, "revision": revision}); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_RESET_FAILED", "重置导演台工程失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_RESET_FAILED", "重置导演台工程失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": revision})
}

func (s *Server) deleteDirectorProject(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_DELETE_FAILED", "删除导演台项目失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `DELETE FROM director_projects WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_DELETE_FAILED", "删除导演台项目失败", true, r)
		return
	}
	if command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "DIRECTOR_PROJECT_NOT_FOUND", "导演台项目不存在", false, r)
		return
	}
	if err = insertAudit(r.Context(), tx, currentSession(r).UserID, "director_project.deleted", "director_project", id.String(), requestIDFromContext(r), map[string]any{}); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_DELETE_FAILED", "删除导演台项目失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "DIRECTOR_PROJECT_DELETE_FAILED", "删除导演台项目失败", true, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
