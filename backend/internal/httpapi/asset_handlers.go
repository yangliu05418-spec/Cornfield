package httpapi

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

type assetResponse struct {
	ID               uuid.UUID  `json:"id"`
	Kind             string     `json:"kind"`
	MediaType        string     `json:"media_type"`
	OriginalFilename *string    `json:"original_filename,omitempty"`
	Width            int        `json:"width"`
	Height           int        `json:"height"`
	ByteSize         int64      `json:"byte_size"`
	SHA256           string     `json:"sha256"`
	URL              string     `json:"url"`
	Thumb320URL      string     `json:"thumb_320_url"`
	Thumb640URL      string     `json:"thumb_640_url"`
	Thumb1280URL     string     `json:"thumb_1280_url"`
	CreatedAt        string     `json:"created_at"`
	BatchID          *uuid.UUID `json:"batch_id,omitempty"`
	JobID            *uuid.UUID `json:"job_id,omitempty"`
	OutputIndex      *int       `json:"output_index,omitempty"`
}

const (
	maxActiveUploadsPerUser = 4
	maxActiveUploadsGlobal  = 32
	maxReservedBytesPerUser = 100 * 1024 * 1024
	maxReservedBytesGlobal  = 1024 * 1024 * 1024
	uploadReservationLock   = int64(4919348247203)
)

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	free, err := storageFreePercent(s.cfg.AssetRoot)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "存储状态无法确认，已暂停新的上传", true, r)
		return
	}
	if free < 15 {
		writeError(w, http.StatusServiceUnavailable, "DISK_PRESSURE", "存储空间不足，已暂停新的上传", true, r)
		return
	}
	var input struct {
		Filename  string `json:"filename"`
		MediaType string `json:"media_type"`
		Size      int64  `json:"size"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Filename = cleanFilename(input.Filename)
	if !validUploadFilename(input.Filename) || input.Size < 1 || input.Size > 25*1024*1024 || !slicesString([]string{"image/jpeg", "image/png", "image/webp"}, input.MediaType) {
		writeError(w, http.StatusUnprocessableEntity, "UPLOAD_UNSUPPORTED", "仅支持 25MiB 以内的 JPEG、PNG 或 WebP", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_CREATE_FAILED", "无法创建上传会话", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize the small reservation decision globally. This prevents many
	// users from simultaneously passing the disk check with unreserved 25MiB
	// streams and bounds both open sessions and outstanding bytes.
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock($1)`, uploadReservationLock); err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_CREATE_FAILED", "无法创建上传会话", true, r)
		return
	}
	var userCount, globalCount int
	var userBytes, globalBytes int64
	err = tx.QueryRow(r.Context(), `SELECT
		count(*) FILTER (WHERE owner_user_id=$1)::int,
		COALESCE(sum(declared_size) FILTER (WHERE owner_user_id=$1),0)::bigint,
		count(*)::int,COALESCE(sum(declared_size),0)::bigint
		FROM upload_sessions WHERE status IN ('created','uploading','validating') AND expires_at>now()`, currentSession(r).UserID).Scan(&userCount, &userBytes, &globalCount, &globalBytes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_CREATE_FAILED", "无法创建上传会话", true, r)
		return
	}
	if userCount >= maxActiveUploadsPerUser || globalCount >= maxActiveUploadsGlobal || userBytes+input.Size > maxReservedBytesPerUser || globalBytes+input.Size > maxReservedBytesGlobal {
		writeError(w, http.StatusTooManyRequests, "UPLOAD_CAPACITY", "上传任务已达并发或容量上限，请稍后重试", true, r)
		return
	}
	var id uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO upload_sessions(owner_user_id,original_filename,declared_media_type,declared_size) VALUES($1,$2,$3,$4) RETURNING id`, currentSession(r).UserID, input.Filename, input.MediaType, input.Size).Scan(&id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "UPLOAD_CREATE_FAILED", "无法创建上传会话", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "created", "content_url": "/api/v1/uploads/" + id.String() + "/content"})
}

func validUploadFilename(value string) bool {
	if !utf8.ValidString(value) || len(value) > 255 || utf8.RuneCountInString(value) > 255 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return value != ""
}

func (s *Server) uploadContent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	sess := currentSession(r)
	var filename, declaredMedia, status string
	var declaredSize int64
	err := s.db.QueryRow(r.Context(), `SELECT original_filename,declared_media_type,declared_size,status FROM upload_sessions WHERE id=$1 AND owner_user_id=$2 AND expires_at>now()`, id, sess.UserID).Scan(&filename, &declaredMedia, &declaredSize, &status)
	if err != nil || status != "created" {
		writeError(w, http.StatusConflict, "UPLOAD_NOT_WRITABLE", "上传会话不存在、已过期或已使用", false, r)
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != declaredSize {
		writeError(w, http.StatusUnprocessableEntity, "UPLOAD_SIZE_MISMATCH", "上传内容大小与会话声明不一致", false, r)
		return
	}
	tempPath := filepath.Join(s.cfg.AssetRoot, "uploads", "quarantine", id.String()+".part")
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		writeError(w, http.StatusConflict, "UPLOAD_IN_PROGRESS", "该上传正在处理", true, r)
		return
	}
	command, err := s.db.Exec(r.Context(), `UPDATE upload_sessions SET status='uploading',quarantine_key=$2,updated_at=now() WHERE id=$1 AND owner_user_id=$3 AND status='created'`, id, filepath.Base(tempPath), sess.UserID)
	if err != nil || command.RowsAffected() != 1 {
		_ = f.Close()
		_ = os.Remove(tempPath)
		writeError(w, http.StatusServiceUnavailable, "UPLOAD_STATE_FAILED", "无法开始上传，请稍后重试", true, r)
		return
	}
	limited := http.MaxBytesReader(w, r.Body, 25*1024*1024)
	written, copyErr := io.Copy(f, limited)
	syncErr := f.Sync()
	closeErr := f.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || written != declaredSize {
		_ = os.Remove(tempPath)
		if _, stateErr := s.db.Exec(r.Context(), `UPDATE upload_sessions SET status='failed',error_code='SIZE_OR_WRITE_INVALID',updated_at=now() WHERE id=$1`, id); stateErr != nil {
			s.log.Warn("upload failure state update failed", "upload_id", id, "error", stateErr)
		}
		writeError(w, http.StatusUnprocessableEntity, "UPLOAD_INVALID", "上传内容大小不符或写入中断", false, r)
		return
	}
	readFile, err := os.Open(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		if _, stateErr := s.db.Exec(r.Context(), `UPDATE upload_sessions SET status='failed',error_code='IMAGE_READ_FAILED',updated_at=now() WHERE id=$1`, id); stateErr != nil {
			s.log.Warn("upload read failure state update failed", "upload_id", id, "error", stateErr)
		}
		writeError(w, http.StatusInternalServerError, "UPLOAD_VALIDATE_FAILED", "无法验证图片", true, r)
		return
	}
	reader := bufio.NewReader(readFile)
	header, _ := reader.Peek(512)
	detected := http.DetectContentType(header)
	readFile.Close()
	if detected != declaredMedia {
		_ = os.Remove(tempPath)
		if _, stateErr := s.db.Exec(r.Context(), `UPDATE upload_sessions SET status='failed',error_code='MIME_MISMATCH',updated_at=now() WHERE id=$1`, id); stateErr != nil {
			s.log.Warn("upload MIME failure state update failed", "upload_id", id, "error", stateErr)
		}
		writeError(w, http.StatusUnprocessableEntity, "MIME_MISMATCH", "图片声明格式与内容不一致", false, r)
		return
	}
	command, err = s.db.Exec(r.Context(), `UPDATE upload_sessions SET status='validating',updated_at=now() WHERE id=$1 AND owner_user_id=$2 AND status='uploading'`, id, sess.UserID)
	if err != nil || command.RowsAffected() != 1 {
		_ = os.Remove(tempPath)
		writeError(w, http.StatusInternalServerError, "UPLOAD_QUEUE_FAILED", "图片已接收但无法进入验证队列", true, r)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "status": "validating"})
}

func (s *Server) getUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var status string
	var assetID *uuid.UUID
	var errorCode *string
	err := s.db.QueryRow(r.Context(), `SELECT status,asset_id,error_code FROM upload_sessions WHERE id=$1 AND owner_user_id=$2`, id, currentSession(r).UserID).Scan(&status, &assetID, &errorCode)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "UPLOAD_NOT_FOUND", "上传会话不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取上传状态失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": status, "asset_id": assetID, "error_code": errorCode})
}

func (s *Server) listAssets(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "分页大小必须在 1 到 100 之间", false, r)
			return
		}
		limit = parsed
	}
	cursorTime, cursorID, err := decodeAssetCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "分页游标无效", false, r)
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT a.id,a.kind,a.media_type,a.original_filename,a.width,a.height,a.byte_size,a.sha256,a.created_at,o.job_id,o.output_index,j.batch_id
		FROM assets a
		LEFT JOIN generation_outputs o ON o.asset_id=a.id
		LEFT JOIN generation_jobs j ON j.id=o.job_id
		WHERE a.owner_user_id=$1 AND a.purged_at IS NULL AND a.purge_pending=false
		  AND ($2::timestamptz IS NULL OR (a.created_at,a.id)<($2,$3::uuid))
		ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, sess.UserID, cursorTime, cursorID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取资产失败", true, r)
		return
	}
	defer rows.Close()
	items := make([]assetResponse, 0)
	for rows.Next() {
		var item assetResponse
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.Kind, &item.MediaType, &item.OriginalFilename, &item.Width, &item.Height, &item.ByteSize, &item.SHA256, &createdAt, &item.JobID, &item.OutputIndex, &item.BatchID); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取资产失败", true, r)
			return
		}
		item.CreatedAt = createdAt.Format(time.RFC3339Nano)
		item.setURLs()
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取资产失败", true, r)
		return
	}
	nextCursor := ""
	if len(items) == limit {
		last := items[len(items)-1]
		createdAt, _ := time.Parse(time.RFC3339Nano, last.CreatedAt)
		nextCursor = encodeAssetCursor(createdAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (s *Server) getAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	item, _, err := s.loadAsset(r, id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "资产不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取资产失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) assetContent(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	item, storageKey, err := s.loadAsset(r, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "ASSET_NOT_FOUND", "资产不存在", false, r)
		return
	}
	variant := r.URL.Query().Get("variant")
	if variant != "" && !slicesString([]string{"320", "640", "1280"}, variant) {
		writeError(w, http.StatusBadRequest, "INVALID_VARIANT", "图片尺寸参数无效", false, r)
		return
	}
	variantReady := variant == ""
	if variant != "" {
		candidate := filepath.ToSlash(filepath.Join(filepath.Dir(storageKey), "thumb-"+variant+".webp"))
		if path, resolveErr := s.blobs.Resolve(candidate); resolveErr == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				storageKey = candidate
				item.MediaType = "image/webp"
				variantReady = true
			}
		}
	}
	w.Header().Set("Content-Type", item.MediaType)
	if variantReady {
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
		etagVariant := variant
		if etagVariant == "" {
			etagVariant = "original"
		}
		etag := `"` + item.SHA256 + `-` + etagVariant + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	} else {
		// Thumbnail generation is deliberately outside the database commit. If
		// the first request wins that short race, serve the original without a
		// durable cache key so it cannot poison the immutable variant URL.
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Cornfield-Variant", "pending")
	}
	w.Header().Set("X-Accel-Redirect", "/_protected_assets/"+storageKey)
	if r.URL.Query().Get("download") == "1" {
		filename := "image" + filepath.Ext(storageKey)
		if item.OriginalFilename != nil {
			filename = cleanFilename(*item.OriginalFilename)
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) loadAsset(r *http.Request, id uuid.UUID) (assetResponse, string, error) {
	sess := currentSession(r)
	var item assetResponse
	var key string
	var createdAt time.Time
	err := s.db.QueryRow(r.Context(), `SELECT a.id,a.kind,a.media_type,a.original_filename,a.width,a.height,a.byte_size,a.sha256,a.storage_key,a.created_at,o.job_id,o.output_index,j.batch_id
		FROM assets a LEFT JOIN generation_outputs o ON o.asset_id=a.id LEFT JOIN generation_jobs j ON j.id=o.job_id
		WHERE a.id=$1 AND a.purged_at IS NULL AND a.purge_pending=false AND (a.owner_user_id=$2 OR $3='admin')`, id, sess.UserID, sess.Role).Scan(&item.ID, &item.Kind, &item.MediaType, &item.OriginalFilename, &item.Width, &item.Height, &item.ByteSize, &item.SHA256, &key, &createdAt, &item.JobID, &item.OutputIndex, &item.BatchID)
	item.CreatedAt = createdAt.Format(time.RFC3339Nano)
	item.setURLs()
	return item, key, err
}

type assetCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        uuid.UUID `json:"id"`
}

func encodeAssetCursor(createdAt time.Time, id uuid.UUID) string {
	payload, _ := json.Marshal(assetCursor{CreatedAt: createdAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

func decodeAssetCursor(raw string) (*time.Time, uuid.UUID, error) {
	if raw == "" {
		return nil, uuid.Nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, uuid.Nil, err
	}
	var cursor assetCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.CreatedAt.IsZero() {
		return nil, uuid.Nil, errInvalidCursor
	}
	return &cursor.CreatedAt, cursor.ID, nil
}

var errInvalidCursor = errors.New("invalid asset cursor")

func (a *assetResponse) setURLs() {
	base := "/api/v1/assets/" + a.ID.String() + "/content"
	a.URL = base
	a.Thumb320URL = base + "?variant=320"
	a.Thumb640URL = base + "?variant=640"
	a.Thumb1280URL = base + "?variant=1280"
}

func slicesString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
