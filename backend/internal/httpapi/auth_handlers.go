package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"internal-image-studio/internal/auth"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Username = auth.NormalizeUsername(input.Username)
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	key := hashForAudit(strings.ToLower(input.Username) + ":" + host)
	if !s.rateLimiter.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "尝试次数过多，请稍后再试", true, r)
		return
	}

	var user struct {
		ID             uuid.UUID
		Username       string
		DisplayName    string
		PasswordHash   string
		Role           string
		Status         string
		MustChange     bool
		TempExpiresAt  *time.Time
		SessionVersion int
	}
	lookupUsername := input.Username
	usernameValid := auth.ValidateUsername(input.Username) == nil
	if !usernameValid {
		// Preserve the same database and Argon2 work for malformed and unknown
		// usernames without sending attacker-controlled megabytes to PostgreSQL.
		lookupUsername = "__invalid_login__"
	}
	err := s.db.QueryRow(r.Context(), `
		SELECT id, username::text, display_name, password_hash, role, status,
		       must_change_password, temporary_password_expires_at, session_version
		FROM users WHERE username = $1`, lookupUsername).Scan(
		&user.ID, &user.Username, &user.DisplayName, &user.PasswordHash, &user.Role,
		&user.Status, &user.MustChange, &user.TempExpiresAt, &user.SessionVersion)
	passwordHash := user.PasswordHash
	if err != nil {
		passwordHash = s.dummyPasswordHash
	}
	passwordCandidate := input.Password
	passwordInputValid := len(passwordCandidate) <= auth.MaximumPasswordBytes
	if !passwordInputValid {
		passwordCandidate = "invalid-password-candidate"
	}
	passwordValid := auth.VerifyPassword(passwordHash, passwordCandidate)
	if err != nil || !usernameValid || !passwordInputValid || user.Status != "active" || !passwordValid {
		s.rateLimiter.Failure(key)
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码不正确", false, r)
		return
	}
	if user.MustChange && user.TempExpiresAt != nil && user.TempExpiresAt.Before(time.Now()) {
		s.rateLimiter.Failure(key)
		writeError(w, http.StatusUnauthorized, "TEMPORARY_PASSWORD_EXPIRED", "临时密码已过期，请联系管理员重置", false, r)
		return
	}
	s.rateLimiter.Success(key)
	token, tokenHash, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_FAILED", "无法创建会话", true, r)
		return
	}
	csrf, csrfHash, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_FAILED", "无法创建会话", true, r)
		return
	}
	expiresAt := time.Now().Add(s.cfg.SessionTTL)
	idleAt := time.Now().Add(s.cfg.SessionIdleTTL)
	ipHash := sha256.Sum256([]byte(host))
	_, err = s.db.Exec(r.Context(), `
		INSERT INTO user_sessions (user_id, token_hash, csrf_hash, session_version, user_agent, ip_hash, expires_at, idle_expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, user.ID, tokenHash, csrfHash,
		user.SessionVersion, r.UserAgent(), ipHash[:], expiresAt, idleAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "无法创建会话", true, r)
		return
	}
	if _, updateErr := s.db.Exec(r.Context(), `UPDATE users SET last_login_at=now(), updated_at=now() WHERE id=$1`, user.ID); updateErr != nil {
		s.log.Warn("last login timestamp update failed", "user_id", user.ID, "error", updateErr)
	}
	s.setSessionCookies(w, token, csrf, expiresAt)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":       map[string]any{"id": user.ID, "username": user.Username, "display_name": user.DisplayName, "role": user.Role, "must_change_password": user.MustChange},
		"csrf_token": csrf,
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": sess.UserID, "username": sess.Username, "display_name": sess.DisplayName,
		"role": sess.Role, "must_change_password": sess.MustChangePassword,
	}})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	if _, err := s.db.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE id=$1`, sess.ID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SESSION_REVOKE_FAILED", "退出登录失败，请重试", true, r)
		return
	}
	s.sessions.Clear()
	s.hub.invalidate(sess.UserID)
	if _, notifyErr := s.db.Exec(r.Context(), `SELECT pg_notify('session_invalidations',$1)`, sess.UserID.String()); notifyErr != nil {
		s.log.Warn("session invalidation notification failed", "user_id", sess.UserID, "error", notifyErr)
	}
	s.clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := auth.ValidatePassword(input.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, "WEAK_PASSWORD", "新密码需为 12–128 个字符", false, r)
		return
	}
	sess := currentSession(r)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", "密码更新失败", true, r)
		return
	}
	defer tx.Rollback(r.Context())
	var currentHash, status string
	var sessionVersion int
	err = tx.QueryRow(r.Context(), `SELECT password_hash,status,session_version FROM users WHERE id=$1 FOR UPDATE`, sess.UserID).Scan(&currentHash, &status, &sessionVersion)
	if err != nil || status != "active" || sessionVersion != sess.SessionVersion {
		writeError(w, http.StatusUnauthorized, "SESSION_INVALID", "会话已失效，请重新登录", false, r)
		return
	}
	if !auth.VerifyPassword(currentHash, input.CurrentPassword) {
		writeError(w, http.StatusUnauthorized, "CURRENT_PASSWORD_INVALID", "当前密码不正确", false, r)
		return
	}
	hash, err := auth.HashPassword(input.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED", "密码更新失败", true, r)
		return
	}
	command, err := tx.Exec(r.Context(), `UPDATE users SET password_hash=$2, must_change_password=false,
		temporary_password_expires_at=NULL, session_version=session_version+1, updated_at=now()
		WHERE id=$1 AND status='active' AND session_version=$3`, sess.UserID, hash, sess.SessionVersion)
	if err == nil && command.RowsAffected() != 1 {
		err = errors.New("password update lost session-version race")
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, sess.UserID)
	}
	if err == nil {
		err = insertAudit(r.Context(), tx, sess.UserID, "user.password_changed", "user", sess.UserID.String(), requestIDFromContext(r), map[string]any{})
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `SELECT pg_notify('session_invalidations',$1)`, sess.UserID.String())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_UPDATE_FAILED", "密码更新失败", true, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_UPDATE_FAILED", "密码更新失败", true, r)
		return
	}
	s.sessions.Clear()
	s.hub.invalidate(sess.UserID)
	s.clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]bool{"reauthenticate": true})
}

func (s *Server) setSessionCookies(w http.ResponseWriter, token, csrf string, expires time.Time) {
	name := "studio_session"
	if s.cfg.CookieSecure {
		name = "__Host-session"
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: "studio_csrf", Value: csrf, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: false, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (s *Server) clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{"studio_session", "__Host-session", "studio_csrf"} {
		http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), HttpOnly: name != "studio_csrf", Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	}
}

func hashForAudit(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func insertAudit(ctx context.Context, tx pgx.Tx, actor uuid.UUID, action, targetType, targetID, requestID string, metadata any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,target_type,target_id,request_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, actor, action, targetType, targetID, requestID, metadata)
	return err
}
