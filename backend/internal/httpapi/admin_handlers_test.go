package httpapi

import "testing"

func TestValidProviderID(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"legnext", "openrouter", "provider_2", "provider-v2"} {
		if !validProviderID(value) {
			t.Fatalf("expected provider ID %q to be valid", value)
		}
	}
	for _, value := range []string{"", "LEGNEXT", "provider/one", "provider one", "../provider"} {
		if validProviderID(value) {
			t.Fatalf("expected provider ID %q to be rejected", value)
		}
	}
}
