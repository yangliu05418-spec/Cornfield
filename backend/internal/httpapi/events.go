package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

const sseSessionRevalidationInterval = time.Minute

type eventHub struct {
	mu          sync.RWMutex
	subscribers map[uuid.UUID]map[chan struct{}]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{subscribers: make(map[uuid.UUID]map[chan struct{}]struct{})}
}

func (h *eventHub) subscribe(userID uuid.UUID, limit int) (<-chan struct{}, func(), bool) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if limit > 0 && len(h.subscribers[userID]) >= limit {
		h.mu.Unlock()
		return nil, func() {}, false
	}
	if h.subscribers[userID] == nil {
		h.subscribers[userID] = make(map[chan struct{}]struct{})
	}
	h.subscribers[userID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subscribers[userID], ch)
		if len(h.subscribers[userID]) == 0 {
			delete(h.subscribers, userID)
		}
		h.mu.Unlock()
	}, true
}

func (h *eventHub) publish(userID uuid.UUID) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subscribers[userID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *eventHub) invalidate(userID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers[userID] {
		close(ch)
	}
	delete(h.subscribers, userID)
}

func (h *eventHub) invalidateAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for userID, subscribers := range h.subscribers {
		for ch := range subscribers {
			close(ch)
		}
		delete(h.subscribers, userID)
	}
}

func (s *Server) listenNotifications(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := s.db.Acquire(ctx)
		if err != nil {
			s.invalidateEventStreams()
			time.Sleep(time.Second)
			continue
		}
		_, err = conn.Exec(ctx, "LISTEN job_events")
		if err != nil {
			conn.Release()
			s.invalidateEventStreams()
			time.Sleep(time.Second)
			continue
		}
		_, err = conn.Exec(ctx, "LISTEN session_invalidations")
		if err != nil {
			conn.Release()
			s.invalidateEventStreams()
			time.Sleep(time.Second)
			continue
		}
		for ctx.Err() == nil {
			notification, waitErr := conn.Conn().WaitForNotification(ctx)
			if waitErr != nil {
				break
			}
			switch notification.Channel {
			case "job_events":
				if userID, parseErr := uuid.Parse(notification.Payload); parseErr == nil {
					s.hub.publish(userID)
				}
			case "session_invalidations":
				s.sessions.Clear()
				if userID, parseErr := uuid.Parse(notification.Payload); parseErr == nil {
					s.hub.invalidate(userID)
				} else {
					s.hub.invalidateAll()
				}
			}
		}
		conn.Release()
		s.invalidateEventStreams()
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Server) invalidateEventStreams() {
	s.sessions.Clear()
	s.hub.invalidateAll()
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "当前连接不支持事件流", false, r)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if queryID, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64); err == nil && queryID > lastID {
		lastID = queryID
	}
	explicitCursor := lastID > 0
	if s.activeSSE.Add(1) > 500 {
		s.activeSSE.Add(-1)
		writeError(w, http.StatusServiceUnavailable, "SSE_CAPACITY", "事件连接已达上限，请稍后重试", true, r)
		return
	}
	defer s.activeSSE.Add(-1)
	sess := currentSession(r)
	wake, unsubscribe, subscribed := s.hub.subscribe(sess.UserID, 4)
	if !subscribed {
		writeError(w, http.StatusTooManyRequests, "SSE_USER_CAPACITY", "当前账号的事件连接已达上限", true, r)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	sessionRevalidation := time.NewTicker(sseSessionRevalidationInterval)
	defer sessionRevalidation.Stop()
	sessionExpiry := time.NewTimer(time.Until(effectiveSessionExpiry(sess)))
	defer sessionExpiry.Stop()

	// A brand-new browser needs only a bounded tail because the page restores
	// current state through the generation and asset APIs. Replaying 90 days of
	// history would make a lost sessionStorage cursor an accidental DoS.
	if !explicitCursor {
		var firstRecentID int64
		if err := s.db.QueryRow(r.Context(), `SELECT COALESCE(min(id),0) FROM (
			SELECT id FROM job_events WHERE owner_user_id=$1 ORDER BY id DESC LIMIT 500
		) recent`, sess.UserID).Scan(&firstRecentID); err != nil {
			return
		}
		if firstRecentID > 0 {
			lastID = firstRecentID - 1
		}
	} else {
		var oldestID, newestID int64
		if err := s.db.QueryRow(r.Context(), `SELECT COALESCE(min(id),0),COALESCE(max(id),0)
			FROM job_events WHERE owner_user_id=$1`, sess.UserID).Scan(&oldestID, &newestID); err != nil {
			return
		}
		if (oldestID > 0 && lastID < oldestID-1) || lastID > newestID {
			lastID = newestID
			payload, _ := json.Marshal(map[string]any{"reason": "cursor_expired", "cursor": newestID})
			if _, err := fmt.Fprintf(w, "id: %d\nevent: reset\ndata: %s\n\n", newestID, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}

	sendPending := func() error {
		for {
			rows, err := s.db.Query(r.Context(), `SELECT id,event_type,batch_id,job_id,payload,created_at FROM job_events WHERE owner_user_id=$1 AND id>$2 ORDER BY id LIMIT 500`, sess.UserID, lastID)
			if err != nil {
				return err
			}
			count := 0
			for rows.Next() {
				var id int64
				var eventType string
				var batchID, jobID *uuid.UUID
				var payload json.RawMessage
				var createdAt time.Time
				if err := rows.Scan(&id, &eventType, &batchID, &jobID, &payload, &createdAt); err != nil {
					rows.Close()
					return err
				}
				data, _ := json.Marshal(map[string]any{"id": id, "type": eventType, "batch_id": batchID, "job_id": jobID, "payload": payload, "created_at": createdAt})
				if _, err := fmt.Fprintf(w, "id: %d\nevent: job\ndata: %s\n\n", id, data); err != nil {
					rows.Close()
					return err
				}
				lastID = id
				count++
			}
			err = rows.Err()
			rows.Close()
			flusher.Flush()
			if err != nil || count < 500 {
				return err
			}
		}
	}
	if err := sendPending(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sessionExpiry.C:
			// A streaming response cannot pass through requireAuth again. Enforce
			// the same hard/idle session boundary on the open SSE connection so a
			// heartbeat cannot keep an expired login alive indefinitely.
			return
		case <-sessionRevalidation.C:
			valid, err := s.sessionStillValid(r.Context(), sess)
			if err != nil || !valid {
				return
			}
		case _, open := <-wake:
			if !open {
				return
			}
			if sendPending() != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) sessionStillValid(ctx context.Context, sess session) (bool, error) {
	var valid bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM user_sessions current_session
		JOIN users current_user ON current_user.id=current_session.user_id
		WHERE current_session.id=$1 AND current_session.user_id=$2
		  AND current_session.revoked_at IS NULL AND current_user.status='active'
		  AND current_session.session_version=$3 AND current_user.session_version=$3
		  AND current_session.expires_at>now() AND current_session.idle_expires_at>now()
	)`, sess.ID, sess.UserID, sess.SessionVersion).Scan(&valid)
	return valid, err
}

func effectiveSessionExpiry(sess session) time.Time {
	if sess.ExpiresAt.Before(sess.IdleExpiresAt) {
		return sess.ExpiresAt
	}
	return sess.IdleExpiresAt
}
