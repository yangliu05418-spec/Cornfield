package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	WorkerServiceName     = "worker"
	WorkerHeartbeatPeriod = 10 * time.Second
	WorkerHeartbeatMaxAge = 45 * time.Second
)

var ErrHeartbeatStale = errors.New("service heartbeat is stale")

type heartbeatExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type heartbeatQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Heartbeat struct {
	DB          heartbeatExecer
	Log         *slog.Logger
	ServiceName string
	InstanceID  string
	Interval    time.Duration
}

func (h *Heartbeat) Run(ctx context.Context) {
	interval := h.Interval
	if interval <= 0 {
		interval = WorkerHeartbeatPeriod
	}
	h.write(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.write(ctx)
		}
	}
}

func (h *Heartbeat) write(ctx context.Context) {
	writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := h.DB.Exec(writeCtx, `INSERT INTO service_heartbeats(service_name,instance_id,started_at,heartbeat_at)
		VALUES($1,$2,now(),now()) ON CONFLICT(service_name,instance_id) DO UPDATE SET heartbeat_at=excluded.heartbeat_at`, h.ServiceName, h.InstanceID)
	if err != nil && ctx.Err() == nil && h.Log != nil {
		h.Log.Warn("worker heartbeat update failed", "error", err)
	}
}

func CheckHeartbeat(ctx context.Context, db heartbeatQuerier, serviceName, instanceID string, maxAge time.Duration) error {
	if serviceName == "" || instanceID == "" || maxAge <= 0 {
		return errors.New("heartbeat check configuration is invalid")
	}
	var heartbeatAt, databaseNow time.Time
	err := db.QueryRow(ctx, `SELECT heartbeat_at,now() FROM service_heartbeats
		WHERE service_name=$1 AND instance_id=$2`, serviceName, instanceID).Scan(&heartbeatAt, &databaseNow)
	if err != nil {
		return fmt.Errorf("read service heartbeat: %w", err)
	}
	if !HeartbeatFresh(heartbeatAt, databaseNow, maxAge) {
		return ErrHeartbeatStale
	}
	return nil
}

func HeartbeatFresh(heartbeatAt, now time.Time, maxAge time.Duration) bool {
	return maxAge > 0 && !heartbeatAt.After(now) && !heartbeatAt.Before(now.Add(-maxAge))
}
