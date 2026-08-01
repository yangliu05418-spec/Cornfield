package httpapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var errProviderUnavailable = errors.New("provider is unavailable")

type providerAvailabilityQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type providerAvailability struct {
	State     string `json:"state"`
	CanSubmit bool   `json:"can_submit"`
	Message   string `json:"message,omitempty"`
}

func loadProviderAvailability(ctx context.Context, db providerAvailabilityQuerier, providerID string, lock bool) (providerAvailability, error) {
	query := `SELECT enabled,state FROM providers WHERE id=$1`
	if lock {
		query += ` FOR SHARE`
	}
	var enabled bool
	var state string
	if err := db.QueryRow(ctx, query, providerID).Scan(&enabled, &state); err != nil {
		return providerAvailability{}, err
	}
	return providerAvailabilityForState(enabled, state), nil
}

func providerAvailabilityForState(enabled bool, state string) providerAvailability {
	if !enabled {
		return providerAvailability{State: "disabled", Message: "该模型当前已停用"}
	}
	availability := providerAvailability{State: state, CanSubmit: state != "paused"}
	if state == "paused" {
		availability.Message = "生成服务暂不可用，请稍后重试"
	} else if state == "degraded" {
		availability.Message = "生成服务当前有波动，任务可能需要等待"
	}
	return availability
}

func requireProviderAvailable(ctx context.Context, db providerAvailabilityQuerier, providerID string) error {
	availability, err := loadProviderAvailability(ctx, db, providerID, true)
	if err != nil {
		return err
	}
	if !availability.CanSubmit {
		return errProviderUnavailable
	}
	return nil
}
