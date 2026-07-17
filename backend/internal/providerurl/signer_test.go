package providerurl

import (
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSignAndVerify(t *testing.T) {
	id := uuid.MustParse("e716a8a2-c1db-4881-9bd0-7aaa7208b55a")
	now := time.Unix(1_700_000_000, 0)
	signed, err := Sign("https://studio.example", "a-long-test-secret", id, ".png", now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify("a-long-test-secret", id, parsed.Path, parsed.Query().Get("expires"), parsed.Query().Get("signature"), now); err != nil {
		t.Fatalf("expected valid signature: %v", err)
	}
}

func TestVerifyRejectsTamperingAndExpiry(t *testing.T) {
	id := uuid.New()
	now := time.Unix(1_700_000_000, 0)
	signed, err := Sign("https://studio.example", "a-long-test-secret", id, ".jpg", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(signed)
	query := parsed.Query()
	if err := Verify("a-long-test-secret", id, parsed.Path+"-tampered", query.Get("expires"), query.Get("signature"), now); err != ErrInvalidSignature {
		t.Fatalf("expected invalid signature, got %v", err)
	}
	if err := Verify("a-long-test-secret", id, parsed.Path, query.Get("expires"), query.Get("signature"), now.Add(2*time.Minute)); err != ErrExpired {
		t.Fatalf("expected expiry, got %v", err)
	}
}
