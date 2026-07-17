package auth

import "testing"

func TestIdentityPolicy(t *testing.T) {
	for _, value := range []string{"admin", "artist.one", "user@example.com", "A-123"} {
		if err := ValidateUsername(value); err != nil {
			t.Fatalf("valid username %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"ab", " space", "bad/name", "控制"} {
		if err := ValidateUsername(value); err == nil {
			t.Fatalf("invalid username %q accepted", value)
		}
	}
	if err := ValidateDisplayName("Cornfield Artist"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateDisplayName("bad\nname"); err == nil {
		t.Fatal("control character was accepted in display name")
	}
}
