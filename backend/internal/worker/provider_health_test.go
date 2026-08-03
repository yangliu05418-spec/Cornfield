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

func TestNextProviderProbeTransitionRequiresConsecutiveHealthyProbes(t *testing.T) {
	transition := nextProviderProbeTransition(
		"paused",
		"PROVIDER_HTTP_403",
		"unknown",
		true,
		false,
		provider.Health{Healthy: true},
	)
	if transition.State != "paused" || transition.ErrorCode != "PROVIDER_HTTP_403" || !transition.PreserveError {
		t.Fatalf("transition = %+v", transition)
	}

	transition = nextProviderProbeTransition(
		"paused",
		"PROVIDER_HTTP_403",
		"healthy",
		true,
		false,
		provider.Health{Healthy: true},
	)
	if transition.State != "healthy" || transition.ErrorCode != "" || transition.PreserveError || !transition.AutoRecovered {
		t.Fatalf("recovery transition = %+v", transition)
	}
}

func TestNextProviderProbeTransitionKeepsBreakerDegraded(t *testing.T) {
	transition := nextProviderProbeTransition("degraded", "", "healthy", true, true, provider.Health{Healthy: true})
	if transition.State != "degraded" || transition.ErrorCode != "" || transition.PreserveError {
		t.Fatalf("transition = %+v", transition)
	}
}

func TestNextProviderProbeTransitionRecoversPausedProviderAsDegradedWhileBreakerOpen(t *testing.T) {
	transition := nextProviderProbeTransition("paused", "PROVIDER_HTTP_403", "healthy", true, true, provider.Health{Healthy: true})
	if transition.State != "degraded" || !transition.AutoRecovered || transition.PreserveError {
		t.Fatalf("transition = %+v", transition)
	}
}

func TestNextProviderProbeTransitionDoesNotRecoverDisabledProvider(t *testing.T) {
	transition := nextProviderProbeTransition("paused", "PROVIDER_HTTP_403", "healthy", false, false, provider.Health{Healthy: true})
	if transition.State != "paused" || !transition.PreserveError || transition.AutoRecovered {
		t.Fatalf("transition = %+v", transition)
	}
}

func TestNextProviderProbeTransitionResetsHealthySequenceAfterFailure(t *testing.T) {
	transition := nextProviderProbeTransition("paused", "PROVIDER_HTTP_403", "healthy", true, false, provider.Health{Message: "timeout"})
	if transition.State != "paused" || !transition.PreserveError || transition.AutoRecovered {
		t.Fatalf("transition = %+v", transition)
	}
}
