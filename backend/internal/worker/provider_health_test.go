package worker

import (
	"testing"

	"internal-image-studio/internal/provider"
)

func TestProviderHealthState(t *testing.T) {
	tests := []struct {
		name   string
		health provider.Health
		state  string
	}{
		{name: "healthy", health: provider.Health{Healthy: true}, state: "healthy"},
		{name: "auth", health: provider.Health{Message: "401 Unauthorized"}, state: "paused"},
		{name: "quota", health: provider.Health{Message: "quota exhausted"}, state: "paused"},
		{name: "network", health: provider.Health{Message: "timeout"}, state: "degraded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, _ := providerHealthState(test.health)
			if state != test.state {
				t.Fatalf("expected %s, got %s", test.state, state)
			}
		})
	}
}
