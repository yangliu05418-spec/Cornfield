package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var httpDurationBuckets = [...]time.Duration{
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	5 * time.Second,
}

type httpMetricKey struct {
	method string
	status int
}

type httpMetrics struct {
	mu       sync.Mutex
	requests map[httpMetricKey]uint64
	buckets  [len(httpDurationBuckets) + 1]uint64
	count    uint64
	sum      time.Duration
}

func newHTTPMetrics() *httpMetrics {
	return &httpMetrics{requests: make(map[httpMetricKey]uint64)}
}

func (m *httpMetrics) record(method string, status int, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[httpMetricKey{method: method, status: status}]++
	m.count++
	m.sum += duration
	for index, upper := range httpDurationBuckets {
		if duration <= upper {
			m.buckets[index]++
		}
	}
	m.buckets[len(m.buckets)-1]++
}

func (s *Server) writeMetrics(parent context.Context, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.metricsData.mu.Lock()
	requests := make(map[httpMetricKey]uint64, len(s.metricsData.requests))
	for key, value := range s.metricsData.requests {
		requests[key] = value
	}
	buckets := s.metricsData.buckets
	count := s.metricsData.count
	sum := s.metricsData.sum
	s.metricsData.mu.Unlock()

	fmt.Fprintln(w, "# TYPE image_studio_http_requests_total counter")
	for key, value := range requests {
		fmt.Fprintf(w, "image_studio_http_requests_total{method=%q,status=%q} %d\n", key.method, strconv.Itoa(key.status), value)
	}
	fmt.Fprintln(w, "# TYPE image_studio_http_request_duration_seconds histogram")
	for index, upper := range httpDurationBuckets {
		fmt.Fprintf(w, "image_studio_http_request_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(upper.Seconds(), 'f', -1, 64), buckets[index])
	}
	fmt.Fprintf(w, "image_studio_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", buckets[len(buckets)-1])
	fmt.Fprintf(w, "image_studio_http_request_duration_seconds_sum %f\n", sum.Seconds())
	fmt.Fprintf(w, "image_studio_http_request_duration_seconds_count %d\n", count)
	fmt.Fprintln(w, "# TYPE image_studio_sse_connections gauge")
	fmt.Fprintf(w, "image_studio_sse_connections %d\n", s.activeSSE.Load())
	if free, err := storageFreePercent(s.cfg.AssetRoot); err == nil {
		fmt.Fprintln(w, "# TYPE image_studio_asset_disk_free_percent gauge")
		fmt.Fprintf(w, "image_studio_asset_disk_free_percent %f\n", free)
	}

	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT status,count(*) FROM generation_jobs GROUP BY status ORDER BY status`)
	if err == nil {
		defer rows.Close()
		fmt.Fprintln(w, "# TYPE image_studio_generation_jobs gauge")
		for rows.Next() {
			var status string
			var total int64
			if rows.Scan(&status, &total) == nil {
				fmt.Fprintf(w, "image_studio_generation_jobs{status=%q} %d\n", status, total)
			}
		}
	}
	var oldestQueuedSeconds float64
	if s.db.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now()-min(created_at)),0) FROM generation_jobs WHERE status='queued'`).Scan(&oldestQueuedSeconds) == nil {
		fmt.Fprintln(w, "# TYPE image_studio_queue_oldest_seconds gauge")
		fmt.Fprintf(w, "image_studio_queue_oldest_seconds %f\n", oldestQueuedSeconds)
	}
	var upstreamActiveLeases int64
	if s.db.QueryRow(ctx, `SELECT count(*)::bigint FROM generation_jobs WHERE upstream_active_until>now()`).Scan(&upstreamActiveLeases) == nil {
		fmt.Fprintln(w, "# TYPE image_studio_upstream_active_leases gauge")
		fmt.Fprintf(w, "image_studio_upstream_active_leases %d\n", upstreamActiveLeases)
	}
	var activeUploads int64
	var reservedUploadBytes int64
	if s.db.QueryRow(ctx, `SELECT count(*)::bigint,COALESCE(sum(declared_size),0)::bigint FROM upload_sessions
		WHERE status IN ('created','uploading','validating') AND expires_at>now()`).Scan(&activeUploads, &reservedUploadBytes) == nil {
		fmt.Fprintln(w, "# TYPE image_studio_upload_sessions gauge")
		fmt.Fprintf(w, "image_studio_upload_sessions %d\n", activeUploads)
		fmt.Fprintln(w, "# TYPE image_studio_upload_reserved_bytes gauge")
		fmt.Fprintf(w, "image_studio_upload_reserved_bytes %d\n", reservedUploadBytes)
	}
	providerRows, err := s.db.Query(ctx, `SELECT id,state,enabled,COALESCE(breaker_open_until>now(),false) FROM providers ORDER BY id`)
	if err == nil {
		fmt.Fprintln(w, "# TYPE image_studio_provider_state gauge")
		fmt.Fprintln(w, "# TYPE image_studio_provider_enabled gauge")
		fmt.Fprintln(w, "# TYPE image_studio_provider_breaker_open gauge")
		for providerRows.Next() {
			var id, state string
			var enabled, breakerOpen bool
			if providerRows.Scan(&id, &state, &enabled, &breakerOpen) == nil {
				fmt.Fprintf(w, "image_studio_provider_state{provider=%q,state=%q} 1\n", id, state)
				fmt.Fprintf(w, "image_studio_provider_enabled{provider=%q} %d\n", id, boolMetric(enabled))
				fmt.Fprintf(w, "image_studio_provider_breaker_open{provider=%q} %d\n", id, boolMetric(breakerOpen))
			}
		}
		providerRows.Close()
	}
	attemptRows, err := s.db.Query(ctx, `SELECT provider_id,outcome,count(*) FROM provider_attempts WHERE created_at>=now()-interval '5 minutes' GROUP BY provider_id,outcome`)
	if err == nil {
		fmt.Fprintln(w, "# TYPE image_studio_provider_attempts_5m gauge")
		for attemptRows.Next() {
			var providerID, outcome string
			var total int64
			if attemptRows.Scan(&providerID, &outcome, &total) == nil {
				fmt.Fprintf(w, "image_studio_provider_attempts_5m{provider=%q,outcome=%q} %d\n", providerID, outcome, total)
			}
		}
		attemptRows.Close()
	}
	heartbeatRows, err := s.db.Query(ctx, `SELECT service_name,EXTRACT(EPOCH FROM now()-max(heartbeat_at))
		FROM service_heartbeats GROUP BY service_name ORDER BY service_name`)
	if err == nil {
		fmt.Fprintln(w, "# TYPE image_studio_service_heartbeat_age_seconds gauge")
		for heartbeatRows.Next() {
			var service string
			var age float64
			if heartbeatRows.Scan(&service, &age) == nil {
				fmt.Fprintf(w, "image_studio_service_heartbeat_age_seconds{service=%q} %f\n", service, age)
			}
		}
		heartbeatRows.Close()
	}
	stats := s.db.Stat()
	fmt.Fprintln(w, "# TYPE image_studio_db_connections gauge")
	fmt.Fprintf(w, "image_studio_db_connections{state=\"acquired\"} %d\n", stats.AcquiredConns())
	fmt.Fprintf(w, "image_studio_db_connections{state=\"idle\"} %d\n", stats.IdleConns())
}

func boolMetric(value bool) int {
	if value {
		return 1
	}
	return 0
}

type responseCapture struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *responseCapture) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCapture) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseCapture) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseCapture) Unwrap() http.ResponseWriter { return w.ResponseWriter }
