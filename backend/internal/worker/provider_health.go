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
			state, code := providerHealthState(health)
			_, err := p.DB.Exec(ctx, `UPDATE providers SET state=CASE
				WHEN NOT enabled THEN state
				WHEN breaker_open_until>now() AND $2='healthy' THEN 'degraded'
				ELSE $2 END,last_probe_at=now(),
				last_error_code=NULLIF($3,''),last_error_at=CASE WHEN $3='' THEN last_error_at ELSE now() END,
				breaker_open_until=CASE WHEN $2='healthy' AND breaker_open_until<=now() THEN NULL ELSE breaker_open_until END,
				updated_at=now() WHERE id=$1`, providerID, state, code)
			if err != nil {
				p.Log.Warn("provider probe state update failed", "provider", providerID, "error", err)
			}
		}()
	}
	group.Wait()
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
