package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/auth"
	"internal-image-studio/internal/blob"
	"internal-image-studio/internal/cache"
	"internal-image-studio/internal/config"
	"internal-image-studio/internal/modelconfig"
)

type Server struct {
	cfg               config.Config
	db                *pgxpool.Pool
	catalog           *modelconfig.Catalog
	blobs             *blob.Local
	log               *slog.Logger
	sessions          *cache.TTL[string, session]
	rateLimiter       *loginLimiter
	hub               *eventHub
	requests          atomic.Uint64
	durationMS        atomic.Uint64
	activeSSE         atomic.Int64
	metricsData       *httpMetrics
	dummyPasswordHash string
}

type session struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	Username           string
	DisplayName        string
	Role               string
	MustChangePassword bool
	SessionVersion     int
	CSRFHash           []byte
	ExpiresAt          time.Time
	IdleExpiresAt      time.Time
}

type contextKey string

const sessionKey contextKey = "session"

func New(ctx context.Context, cfg config.Config, db *pgxpool.Pool, catalog *modelconfig.Catalog, blobs *blob.Local, logger *slog.Logger) *Server {
	dummyPasswordHash, _ := auth.HashPassword(uuid.NewString())
	server := &Server{
		cfg:               cfg,
		db:                db,
		catalog:           catalog,
		blobs:             blobs,
		log:               logger,
		sessions:          cache.NewTTL[string, session](10_000),
		rateLimiter:       newLoginLimiter(),
		hub:               newEventHub(),
		metricsData:       newHTTPMetrics(),
		dummyPasswordHash: dummyPasswordHash,
	}
	go server.listenNotifications(ctx)
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("GET /api/v1/auth/me", s.requireAuth(http.HandlerFunc(s.me)))
	mux.Handle("POST /api/v1/auth/logout", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.logout))))
	mux.Handle("POST /api/v1/auth/change-password", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.changePassword))))
	mux.Handle("GET /api/v1/models", s.requireAuth(http.HandlerFunc(s.models)))
	mux.Handle("GET /api/v1/generations", s.requireAuth(http.HandlerFunc(s.listGenerations)))
	mux.Handle("POST /api/v1/generations", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.createGeneration))))
	mux.Handle("GET /api/v1/generations/{id}", s.requireAuth(http.HandlerFunc(s.getGeneration)))
	mux.Handle("POST /api/v1/generations/{id}/cancel", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.cancelBatch))))
	mux.Handle("POST /api/v1/generations/{id}/retry", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.retryBatch))))
	mux.Handle("POST /api/v1/generations/{id}/jobs/{jobID}/cancel", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.cancelJob))))
	mux.Handle("DELETE /api/v1/generations/{id}/jobs/{jobID}", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.dismissJob))))
	mux.Handle("GET /api/v1/events", s.requireAuth(http.HandlerFunc(s.events)))
	mux.Handle("POST /api/v1/uploads", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.createUpload))))
	mux.Handle("PUT /api/v1/uploads/{id}/content", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.uploadContent))))
	mux.Handle("GET /api/v1/uploads/{id}", s.requireAuth(http.HandlerFunc(s.getUpload)))
	mux.Handle("GET /api/v1/assets", s.requireAuth(http.HandlerFunc(s.listAssets)))
	mux.Handle("GET /api/v1/asset-folders", s.requireAuth(http.HandlerFunc(s.listAssetFolders)))
	mux.Handle("POST /api/v1/asset-folders", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.createAssetFolder))))
	mux.Handle("PATCH /api/v1/asset-folders/{id}", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.updateAssetFolder))))
	mux.Handle("DELETE /api/v1/asset-folders/{id}", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.deleteAssetFolder))))
	mux.Handle("GET /api/v1/assets/{id}", s.requireAuth(http.HandlerFunc(s.getAsset)))
	mux.Handle("PATCH /api/v1/assets/{id}/organization", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.organizeAsset))))
	mux.Handle("GET /api/v1/assets/{id}/content", s.requireAuth(http.HandlerFunc(s.assetContent)))
	mux.Handle("DELETE /api/v1/assets/{id}", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.deleteAsset))))
	mux.Handle("GET /api/v1/deletions/{id}", s.requireAuth(http.HandlerFunc(s.getDeletion)))
	mux.HandleFunc("GET /api/v1/provider-assets/{id}/{filename}", s.providerAsset)
	mux.HandleFunc("HEAD /api/v1/provider-assets/{id}/{filename}", s.providerAsset)
	mux.Handle("GET /api/v1/admin/users", s.requireAdmin(http.HandlerFunc(s.listUsers)))
	mux.Handle("POST /api/v1/admin/users", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.createUser))))
	mux.Handle("PATCH /api/v1/admin/users/{id}", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.updateUser))))
	mux.Handle("POST /api/v1/admin/users/{id}/reset-password", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.resetPassword))))
	mux.Handle("POST /api/v1/admin/users/{id}/revoke-sessions", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.revokeSessions))))
	mux.Handle("POST /api/v1/admin/users/{id}/deletion", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.deleteUser))))
	mux.Handle("GET /api/v1/admin/models", s.requireAdmin(http.HandlerFunc(s.adminModels)))
	mux.Handle("GET /api/v1/admin/providers", s.requireAdmin(http.HandlerFunc(s.providers)))
	mux.Handle("POST /api/v1/admin/providers/{id}/resume", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.resumeProvider))))
	mux.Handle("POST /api/v1/admin/jobs/{id}/reconcile-submission", s.requireAdmin(s.requireCSRF(http.HandlerFunc(s.reconcileSubmission))))
	mux.HandleFunc("POST /api/v1/provider-callbacks/legnext/{jobID}/{signature}", s.legnextCallback)
	return s.requestMiddleware(mux)
}

func (s *Server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if parsed, err := uuid.Parse(requestID); err == nil {
			requestID = parsed.String()
		} else {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		captured := &responseCapture{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(captured, r.WithContext(context.WithValue(r.Context(), contextKey("request_id"), requestID)))
		duration := time.Since(start).Milliseconds()
		s.requests.Add(1)
		s.durationMS.Add(uint64(max(duration, 0)))
		s.metricsData.record(r.Method, captured.status, time.Duration(duration)*time.Millisecond)
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/v1/provider-callbacks/") {
			path = "/api/v1/provider-callbacks/[redacted]"
		}
		s.log.Info("http request", "request_id", requestID, "method", r.Method, "path", path, "status", captured.status, "duration_ms", duration)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("__Host-session")
		if err != nil && !s.cfg.CookieSecure {
			cookie, err = r.Cookie("studio_session")
		}
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "请先登录", false, r)
			return
		}
		sess, err := s.lookupSession(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "SESSION_INVALID", "登录已失效，请重新登录", false, r)
			return
		}
		if sess.MustChangePassword && r.URL.Path != "/api/v1/auth/me" && r.URL.Path != "/api/v1/auth/change-password" && r.URL.Path != "/api/v1/auth/logout" {
			writeError(w, http.StatusForbidden, "PASSWORD_CHANGE_REQUIRED", "首次登录需要先修改密码", false, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if currentSession(r).Role != "admin" {
			writeError(w, http.StatusForbidden, "ADMIN_REQUIRED", "需要管理员权限", false, r)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := currentSession(r)
		got := auth.HashToken(r.Header.Get("X-CSRF-Token"))
		if len(sess.CSRFHash) == 0 || subtle.ConstantTimeCompare(got, sess.CSRFHash) != 1 {
			writeError(w, http.StatusForbidden, "CSRF_INVALID", "页面安全令牌已失效，请刷新后重试", false, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) lookupSession(ctx context.Context, plain string) (session, error) {
	cacheKey := fmt.Sprintf("%x", auth.HashToken(plain))
	if sess, ok := s.sessions.Get(cacheKey); ok {
		return sess, nil
	}
	var sess session
	err := s.db.QueryRow(ctx, `
		SELECT s.id, u.id, u.username::text, u.display_name, u.role, u.must_change_password,
		       u.session_version, s.csrf_hash, s.expires_at, s.idle_expires_at
		FROM user_sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.revoked_at IS NULL AND u.status = 'active'
		  AND s.session_version = u.session_version AND s.expires_at > now() AND s.idle_expires_at > now()`,
		auth.HashToken(plain)).Scan(&sess.ID, &sess.UserID, &sess.Username, &sess.DisplayName, &sess.Role,
		&sess.MustChangePassword, &sess.SessionVersion, &sess.CSRFHash, &sess.ExpiresAt, &sess.IdleExpiresAt)
	if err != nil {
		return session{}, err
	}
	if err := s.db.QueryRow(ctx, `UPDATE user_sessions SET last_seen_at = now(), idle_expires_at = LEAST(expires_at, now() + $2::interval)
		WHERE id = $1 RETURNING idle_expires_at`, sess.ID, pgInterval(s.cfg.SessionIdleTTL)).Scan(&sess.IdleExpiresAt); err != nil {
		return session{}, err
	}
	s.sessions.Set(cacheKey, sess, 30*time.Second)
	return sess, nil
}

func currentSession(r *http.Request) session {
	return r.Context().Value(sessionKey).(session)
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if failure := s.checkReadiness(ctx); failure != nil {
		writeError(w, http.StatusServiceUnavailable, failure.code, failure.message, true, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "model_revision": s.catalog.Hash})
}

type readinessFailure struct {
	code    string
	message string
}

func (s *Server) checkReadiness(ctx context.Context) *readinessFailure {
	if err := s.db.Ping(ctx); err != nil {
		return &readinessFailure{code: "DATABASE_UNAVAILABLE", message: "数据库尚未就绪"}
	}
	if _, err := storageFreePercent(s.cfg.AssetRoot); err != nil {
		return &readinessFailure{code: "STORAGE_UNAVAILABLE", message: "存储尚未就绪"}
	}
	var appliedModels int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM models WHERE current_revision=$1`, s.catalog.Hash).Scan(&appliedModels); err != nil || appliedModels != len(s.catalog.Models) {
		return &readinessFailure{code: "MODEL_CONFIG_NOT_APPLIED", message: "模型配置尚未应用"}
	}
	var appliedSnapshots int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM models m JOIN model_capability_versions v
		ON v.model_id=m.id AND v.revision=m.current_revision WHERE m.current_revision=$1`, s.catalog.Hash).Scan(&appliedSnapshots); err != nil || appliedSnapshots != len(s.catalog.Models) {
		return &readinessFailure{code: "MODEL_SNAPSHOT_NOT_APPLIED", message: "模型能力快照尚未完整应用"}
	}
	return nil
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	s.writeMetrics(r.Context(), w)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求内容格式不正确", false, r)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "请求只能包含一个 JSON 对象", false, r)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string, retryable bool, r *http.Request) {
	requestID := requestIDFromContext(r)
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message, "retryable": retryable, "request_id": requestID},
	})
}

func requestIDFromContext(r *http.Request) string {
	requestID, _ := r.Context().Value(contextKey("request_id")).(string)
	return requestID
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "资源编号不正确", false, r)
		return uuid.Nil, false
	}
	return id, true
}

func pgInterval(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10) + " seconds"
}

func isNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func cleanFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, `"`, "")
	if name == "" {
		return "image"
	}
	return name
}

type loginLimiter struct {
	entries *cache.TTL[string, int]
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: cache.NewTTL[string, int](10_000)}
}

func (l *loginLimiter) Allow(key string) bool {
	count, _ := l.entries.Get(key)
	return count < 10
}

func (l *loginLimiter) Failure(key string) {
	count, _ := l.entries.Get(key)
	l.entries.Set(key, count+1, 15*time.Minute)
}

func (l *loginLimiter) Success(key string) { l.entries.Delete(key) }
