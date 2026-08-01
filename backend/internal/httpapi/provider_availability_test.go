package httpapi

import "testing"

func TestProviderAvailabilityForState(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		state     string
		canSubmit bool
		wantState string
	}{
		{name: "disabled", enabled: false, state: "healthy", wantState: "disabled"},
		{name: "paused", enabled: true, state: "paused", wantState: "paused"},
		{name: "unknown", enabled: true, state: "unknown", canSubmit: true, wantState: "unknown"},
		{name: "degraded", enabled: true, state: "degraded", canSubmit: true, wantState: "degraded"},
		{name: "healthy", enabled: true, state: "healthy", canSubmit: true, wantState: "healthy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := providerAvailabilityForState(test.enabled, test.state)
			if got.State != test.wantState || got.CanSubmit != test.canSubmit {
				t.Fatalf("availability = %+v, want state %q can_submit=%v", got, test.wantState, test.canSubmit)
			}
		})
	}
}
