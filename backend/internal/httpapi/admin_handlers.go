package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"internal-image-studio/internal/auth"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT id,username::text,display_name,role,status,must_change_password,last_login_at,created_at FROM users ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败", true, r)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var username, displayName, role, status string
		var mustChange bool
		var lastLogin *time.Time
		var created time.Time
		if err := rows.Scan(&id, &username, &displayName, &role, &status, &mustChange, &lastLogin, &created); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败", true, r)
			return
		}
		items = append(items, map[string]any{"id": id, "username": username, "display_name": displayName, "role": role, "status": status, "must_change_password": mustChange, "last_login_at": lastLogin, "created_at": created})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取用户失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Role        string `json:"role"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = auth.NormalizeUsername(input.Username)
	input.DisplayName = auth.NormalizeDisplayName(input.DisplayName)
	if auth.ValidateUsername(input.Username) != nil || auth.ValidateDisplayName(input.DisplayName) != nil || (input.Role != "member" && input.Role != "admin") {
		writeError(w, http.StatusUnprocessableEntity, "USER_INVALID", "用户名、显示名或角色无效", false, r)
		return
	}
	temporary, err := temporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_GENERATION_FAILED", "Unable to create a secure temporary password", true, r)
		return
	}
	hash, err := auth.HashPassword(temporary)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED", "用户创建失败", true, r)
		return
	}
	actor := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER_CREATE_FAILED", "用户创建失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var id uuid.UUID
	err = tx.QueryRow(r.Context(), `INSERT INTO users(username,display_name,password_hash,role,must_change_password,temporary_password_expires_at,created_by)
		VALUES($1,$2,$3,$4,true,now()+interval '24 hours',$5) RETURNING id`, input.Username, input.DisplayName, hash, input.Role, actor.UserID).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeError(w, http.StatusConflict, "USERNAME_EXISTS", "用户名已存在", false, r)
		} else {
			writeError(w, http.StatusInternalServerError, "USER_CREATE_FAILED", "用户创建失败", true, r)
		}
		return
	}
	if err = insertAudit(r.Context(), tx, actor.UserID, "user.create", "user", id.String(), requestIDFromContext(r), map[string]any{"role": input.Role}); err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_WRITE_FAILED", "用户创建审计记录失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "USER_CREATE_FAILED", "用户创建失败", true, r)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "username": input.Username, "temporary_password": temporary, "expires_in_hours": 24})
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Status      *string `json:"status"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.DisplayName != nil {
		normalized := auth.NormalizeDisplayName(*input.DisplayName)
		input.DisplayName = &normalized
		if auth.ValidateDisplayName(normalized) != nil {
			writeError(w, http.StatusUnprocessableEntity, "USER_INVALID", "显示名无效", false, r)
			return
		}
	}
	if input.Role != nil && *input.Role != "member" && *input.Role != "admin" || input.Status != nil && *input.Status != "active" && *input.Status != "disabled" {
		writeError(w, http.StatusUnprocessableEntity, "USER_INVALID", "角色或状态无效", false, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "USER_UPDATE_FAILED", "用户更新失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	if input.Role != nil || input.Status != nil {
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock($1)`, int64(4919348247202)); err != nil {
			writeError(w, http.StatusInternalServerError, "USER_UPDATE_FAILED", "用户更新失败", true, r)
			return
		}
	}
	var currentRole, currentStatus string
	if err := tx.QueryRow(r.Context(), `SELECT role,status FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&currentRole, &currentStatus); err != nil {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在", false, r)
		return
	}
	disablesAdmin := currentRole == "admin" && ((input.Role != nil && *input.Role != "admin") || (input.Status != nil && *input.Status != "active")) && currentStatus == "active"
	if disablesAdmin {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE role='admin' AND status='active'`).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "USER_UPDATE_FAILED", "用户更新失败", true, r)
			return
		}
		if count <= 1 {
			writeError(w, http.StatusConflict, "LAST_ADMIN_PROTECTED", "不能停用或降级最后一名有效管理员", false, r)
			return
		}
	}
	command, err := tx.Exec(r.Context(), `UPDATE users SET display_name=COALESCE($2,display_name),role=COALESCE($3,role),status=COALESCE($4,status),
		session_version=CASE WHEN $3 IS NOT NULL OR $4 IS NOT NULL THEN session_version+1 ELSE session_version END,updated_at=now() WHERE id=$1`, id, input.DisplayName, input.Role, input.Status)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, http.StatusInternalServerError, "USER_UPDATE_FAILED", "用户更新失败", true, r)
		return
	}
	if input.Role != nil || input.Status != nil {
		_, err = tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
		if err == nil {
			_, err = tx.Exec(r.Context(), `SELECT pg_notify('session_invalidations',$1)`, id.String())
		}
	}
	actor := currentSession(r)
	if err == nil {
		err = insertAudit(r.Context(), tx, actor.UserID, "user.update", "user", id.String(), requestIDFromContext(r), input)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "USER_UPDATE_FAILED", "用户更新失败", true, r)
		return
	}
	if input.Role != nil || input.Status != nil {
		s.sessions.Clear()
		s.hub.invalidate(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	temporary, err := temporaryPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_GENERATION_FAILED", "Unable to create a secure temporary password", true, r)
		return
	}
	hash, err := auth.HashPassword(temporary)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED", "密码重置失败", true, r)
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_RESET_FAILED", "密码重置失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	command, err := tx.Exec(r.Context(), `UPDATE users SET password_hash=$2,must_change_password=true,temporary_password_expires_at=now()+interval '24 hours',session_version=session_version+1,updated_at=now() WHERE id=$1`, id, hash)
	if err != nil || command.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在", false, r)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id); err == nil {
		_, err = tx.Exec(r.Context(), `SELECT pg_notify('session_invalidations',$1)`, id.String())
	}
	actor := currentSession(r)
	if err == nil {
		err = insertAudit(r.Context(), tx, actor.UserID, "user.reset_password", "user", id.String(), requestIDFromContext(r), map[string]any{})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_RESET_FAILED", "密码重置失败", true, r)
		return
	}
	s.sessions.Clear()
	s.hub.invalidate(id)
	writeJSON(w, http.StatusOK, map[string]any{"temporary_password": temporary, "expires_in_hours": 24})
}

func (s *Server) revokeSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "会话撤销失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var targetUser uuid.UUID
	if err = tx.QueryRow(r.Context(), `SELECT id FROM users WHERE id=$1 FOR SHARE`, id).Scan(&targetUser); err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "USER_NOT_FOUND", "用户不存在", false, r)
		} else {
			writeError(w, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "会话撤销失败", true, r)
		}
		return
	}
	command, err := tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "会话撤销失败", true, r)
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_notify('session_invalidations',$1)`, id.String()); err == nil {
		actor := currentSession(r)
		err = insertAudit(r.Context(), tx, actor.UserID, "user.revoke_sessions", "user", id.String(), requestIDFromContext(r), map[string]any{"revoked": command.RowsAffected()})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_REVOKE_FAILED", "会话撤销失败", true, r)
		return
	}
	s.sessions.Clear()
	s.hub.invalidate(id)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": command.RowsAffected()})
}

func (s *Server) adminModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"revision": s.catalog.Hash, "models": s.catalog.Models})
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `SELECT p.id,p.display_name,p.enabled,p.state,p.breaker_open_until,p.last_probe_at,p.last_error_code,p.last_error_at,
		(SELECT count(*) FROM generation_jobs j
		 JOIN generation_batches b ON b.id=j.batch_id
		 JOIN model_capability_versions v ON v.model_id=b.model_id AND v.revision=b.capability_revision
		 WHERE v.config->>'provider'=p.id
		   AND (j.status IN ('dispatched','submitting','provider_pending','ingesting','cancelling') OR j.upstream_active_until>now())) active_jobs
		FROM providers p ORDER BY p.id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取上游状态失败", true, r)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, state string
		var enabled bool
		var breaker, probe, lastErrorAt *time.Time
		var errorCode *string
		var active int
		if err := rows.Scan(&id, &name, &enabled, &state, &breaker, &probe, &errorCode, &lastErrorAt, &active); err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取上游状态失败", true, r)
			return
		}
		items = append(items, map[string]any{"id": id, "display_name": name, "enabled": enabled, "state": state, "breaker_open_until": breaker, "last_probe_at": probe, "last_error_code": errorCode, "last_error_at": lastErrorAt, "active_jobs": active})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "读取上游状态失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) resumeProvider(w http.ResponseWriter, r *http.Request) {
	providerID := strings.TrimSpace(r.PathValue("id"))
	if !validProviderID(providerID) {
		writeError(w, http.StatusBadRequest, "PROVIDER_ID_INVALID", "上游编号不正确", false, r)
		return
	}

	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PROVIDER_RESUME_FAILED", "上游恢复失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())

	var enabled bool
	var state string
	var previousErrorCode *string
	err = tx.QueryRow(r.Context(), `SELECT enabled,state,last_error_code FROM providers WHERE id=$1 FOR UPDATE`, providerID).
		Scan(&enabled, &state, &previousErrorCode)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "PROVIDER_NOT_FOUND", "上游不存在", false, r)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PROVIDER_RESUME_FAILED", "上游恢复失败", true, r)
		return
	}
	if !enabled {
		writeError(w, http.StatusConflict, "PROVIDER_DISABLED", "已停用的上游不能恢复", false, r)
		return
	}
	if state != "paused" {
		writeError(w, http.StatusConflict, "PROVIDER_NOT_PAUSED", "上游当前没有暂停", false, r)
		return
	}

	if _, err = tx.Exec(r.Context(), `UPDATE providers SET state='degraded',breaker_open_until=NULL,
		last_error_code=NULL,last_error_at=NULL,updated_at=now() WHERE id=$1`, providerID); err != nil {
		writeError(w, http.StatusInternalServerError, "PROVIDER_RESUME_FAILED", "上游恢复失败", true, r)
		return
	}
	metadata := map[string]any{"previous_state": state}
	if previousErrorCode != nil {
		metadata["previous_error_code"] = *previousErrorCode
	}
	actor := currentSession(r)
	if err = insertAudit(r.Context(), tx, actor.UserID, "provider.resume", "provider", providerID, requestIDFromContext(r), metadata); err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_WRITE_FAILED", "上游恢复审计记录失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "PROVIDER_RESUME_FAILED", "上游恢复失败", true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": providerID, "state": "degraded", "resumed": true})
}

func validProviderID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func temporaryPassword() (string, error) {
	return temporaryPasswordFrom(rand.Reader)
}

func temporaryPasswordFrom(reader io.Reader) (string, error) {
	b := make([]byte, 18)
	if _, err := io.ReadFull(reader, b); err != nil {
		return "", err
	}
	return "T9!" + base64.RawURLEncoding.EncodeToString(b), nil
}
