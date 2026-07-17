package worker

import (
	"testing"
	"time"
)

func TestBreakerOpensAtFailureThreshold(t *testing.T) {
	breaker := NewBreaker()
	for index := 0; index < 9; index++ {
		opened, _ := breaker.Record("provider:model", index%2 == 0)
		if opened {
			t.Fatal("breaker opened before minimum sample count")
		}
	}
	opened, _ := breaker.Record("provider:model", false)
	if !opened || breaker.Allow("provider:model") {
		t.Fatal("breaker did not open at threshold")
	}
}

func TestBreakerUsesModelPolicy(t *testing.T) {
	breaker := NewBreaker()
	opened, until := breaker.RecordPolicy("provider:model", false, 1, 1, 2*time.Second)
	if !opened {
		t.Fatal("breaker ignored model-specific minimum")
	}
	remaining := time.Until(until)
	if remaining < time.Second || remaining > 3*time.Second {
		t.Fatalf("unexpected policy cooldown: %s", remaining)
	}
}

func TestBreakerAbandonsHalfOpenReservation(t *testing.T) {
	breaker := NewBreaker()
	_, _ = breaker.RecordPolicy("provider:model", false, 1, 1, time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	if !breaker.Allow("provider:model") {
		t.Fatal("expected half-open probe")
	}
	breaker.Abandon("provider:model")
	if !breaker.Allow("provider:model") {
		t.Fatal("abandoned half-open probe remained reserved")
	}
}

func TestPassiveTrafficDoesNotConsumeHalfOpenProbe(t *testing.T) {
	breaker := NewBreaker()
	_, _ = breaker.RecordPolicy("provider:model", false, 1, 1, time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	if !breaker.Allow("provider:model") {
		t.Fatal("expected half-open probe")
	}
	opened, _ := breaker.RecordPassivePolicy("provider:model", true, 1, 1, time.Millisecond)
	if !opened || breaker.Allow("provider:model") {
		t.Fatal("passive poll consumed the reserved half-open probe")
	}
	opened, _ = breaker.RecordPolicy("provider:model", true, 1, 1, time.Millisecond)
	if opened || !breaker.Allow("provider:model") {
		t.Fatal("successful active probe did not close breaker")
	}
}

func TestBreakerForceOpen(t *testing.T) {
	breaker := NewBreaker()
	if !breaker.Allow("provider:model") {
		t.Fatal("fresh breaker should allow")
	}
	until := breaker.ForceOpen("provider:model", time.Minute)
	if !until.After(time.Now()) {
		t.Fatal("forced breaker deadline must be in the future")
	}
	if breaker.Allow("provider:model") {
		t.Fatal("forced breaker must reject new submissions")
	}
}
