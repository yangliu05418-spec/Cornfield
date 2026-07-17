package worker

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"internal-image-studio/internal/provider"
)

type ProviderProber struct {
	DB       *pgxpool.Pool
	Adapters map[string]provider.Adapter
	Log      *slog.Logger
	Interval time.Duration
}

func (p *ProviderProber) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p.probeAll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probeAll(ctx)
		}
	}
}

func (p *ProviderProber) probeAll(ctx context.Context) {
	var group sync.WaitGroup
	for providerID, adapter := range p.Adapters {
		providerID, adapter := providerID, adapter
		group.Add(1)
		go func() {
			defer group.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			health := adapter.Probe(probeCtx)
			err := p.persistProbe(ctx, providerID, health)
			if err != nil {
				p.Log.Warn("provider probe state update failed", "provider", providerID, "error", err)
			}
		}()
	}
	group.Wait()
}

type providerProbeTransition struct {
	State         string
	ErrorCode     string
	PreserveError bool
}

func nextProviderProbeTransition(currentState, currentErrorCode string, enabled, breakerOpen bool, health provider.Health) providerProbeTransition {
	probeState, probeCode := providerHealthState(health)
	if currentState == "paused" {
		return providerProbeTransition{State: currentState, ErrorCode: currentErrorCode, PreserveError: true}
	}
	if !enabled {
		return providerProbeTransition{State: currentState, ErrorCode: probeCode}
	}
	if breakerOpen && probeState == "healthy" {
		probeState = "degraded"
	}
	return providerProbeTransition{State: probeState, ErrorCode: probeCode}
}

func (p *ProviderProber) persistProbe(ctx context.Context, providerID string, health provider.Health) error {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var currentState, currentErrorCode string
	var enabled, breakerOpen bool
	if err := tx.QueryRow(ctx, `SELECT state,COALESCE(last_error_code,''),enabled,
		COALESCE(breaker_open_until>now(),false) FROM providers WHERE id=$1 FOR UPDATE`, providerID).
		Scan(&currentState, &currentErrorCode, &enabled, &breakerOpen); err != nil {
		return err
	}
	transition := nextProviderProbeTransition(currentState, currentErrorCode, enabled, breakerOpen, health)
	if transition.PreserveError {
		_, err = tx.Exec(ctx, `UPDATE providers SET last_probe_at=now(),updated_at=now() WHERE id=$1`, providerID)
	} else {
		probeState, _ := providerHealthState(health)
		_, err = tx.Exec(ctx, `UPDATE providers SET state=$2,last_probe_at=now(),
			last_error_code=NULLIF($3,''),last_error_at=CASE WHEN $3='' THEN last_error_at ELSE now() END,
			breaker_open_until=CASE WHEN $4='healthy' AND breaker_open_until<=now() THEN NULL ELSE breaker_open_until END,
			updated_at=now() WHERE id=$1`, providerID, transition.State, transition.ErrorCode, probeState)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func providerHealthState(health provider.Health) (state, code string) {
	if health.Healthy {
		return "healthy", ""
	}
	message := strings.ToLower(health.Message)
	if strings.Contains(message, "401") || strings.Contains(message, "402") || strings.Contains(message, "403") || strings.Contains(message, "quota") || strings.Contains(message, "balance") {
		return "paused", "PROVIDER_AUTH_OR_QUOTA"
	}
	return "degraded", "PROVIDER_PROBE_FAILED"
}
