package worker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestHeartbeatFresh(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		heartbeatAt time.Time
		maxAge      time.Duration
		want        bool
	}{
		{name: "recent", heartbeatAt: now.Add(-10 * time.Second), maxAge: 45 * time.Second, want: true},
		{name: "boundary", heartbeatAt: now.Add(-45 * time.Second), maxAge: 45 * time.Second, want: true},
		{name: "stale", heartbeatAt: now.Add(-46 * time.Second), maxAge: 45 * time.Second},
		{name: "future timestamp", heartbeatAt: now.Add(time.Second), maxAge: 45 * time.Second},
		{name: "invalid max age", heartbeatAt: now, maxAge: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HeartbeatFresh(test.heartbeatAt, now, test.maxAge); got != test.want {
				t.Fatalf("HeartbeatFresh = %t, want %t", got, test.want)
			}
		})
	}
}

func TestHeartbeatWriteUsesStableIdentity(t *testing.T) {
	db := &fakeHeartbeatExecer{}
	heartbeat := &Heartbeat{DB: db, ServiceName: WorkerServiceName, InstanceID: "instance-a"}
	heartbeat.write(context.Background())
	if !strings.Contains(db.sql, "ON CONFLICT(service_name,instance_id)") {
		t.Fatalf("SQL does not upsert heartbeat: %s", db.sql)
	}
	if len(db.args) != 2 || db.args[0] != WorkerServiceName || db.args[1] != "instance-a" {
		t.Fatalf("args = %#v", db.args)
	}
}

func TestCheckHeartbeat(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		row       pgx.Row
		wantError error
	}{
		{name: "fresh", row: fakeHeartbeatRow{heartbeatAt: now.Add(-10 * time.Second), databaseNow: now}},
		{name: "stale", row: fakeHeartbeatRow{heartbeatAt: now.Add(-46 * time.Second), databaseNow: now}, wantError: ErrHeartbeatStale},
		{name: "query error", row: fakeHeartbeatRow{err: pgx.ErrNoRows}, wantError: pgx.ErrNoRows},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := &fakeHeartbeatQuerier{row: test.row}
			err := CheckHeartbeat(context.Background(), db, WorkerServiceName, "instance-a", 45*time.Second)
			if test.wantError == nil && err != nil {
				t.Fatalf("CheckHeartbeat: %v", err)
			}
			if test.wantError != nil && !errors.Is(err, test.wantError) {
				t.Fatalf("CheckHeartbeat error = %v, want %v", err, test.wantError)
			}
			if len(db.args) != 2 || db.args[0] != WorkerServiceName || db.args[1] != "instance-a" {
				t.Fatalf("query args = %#v", db.args)
			}
		})
	}
}

type fakeHeartbeatExecer struct {
	sql  string
	args []any
}

func (f *fakeHeartbeatExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql = sql
	f.args = args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

type fakeHeartbeatQuerier struct {
	row  pgx.Row
	args []any
}

func (f *fakeHeartbeatQuerier) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	f.args = args
	return f.row
}

type fakeHeartbeatRow struct {
	heartbeatAt time.Time
	databaseNow time.Time
	err         error
}

func (f fakeHeartbeatRow) Scan(destinations ...any) error {
	if f.err != nil {
		return f.err
	}
	*(destinations[0].(*time.Time)) = f.heartbeatAt
	*(destinations[1].(*time.Time)) = f.databaseNow
	return nil
}
