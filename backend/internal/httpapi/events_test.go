package httpapi

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestEventHubEnforcesUserLimitAndInvalidates(t *testing.T) {
	hub := newEventHub()
	userID := uuid.New()
	first, unsubscribeFirst, ok := hub.subscribe(userID, 2)
	if !ok {
		t.Fatal("first subscription was rejected")
	}
	defer unsubscribeFirst()
	_, unsubscribeSecond, ok := hub.subscribe(userID, 2)
	if !ok {
		t.Fatal("second subscription was rejected")
	}
	defer unsubscribeSecond()
	if _, _, ok := hub.subscribe(userID, 2); ok {
		t.Fatal("subscription above the per-user limit was accepted")
	}

	hub.publish(userID)
	if _, open := <-first; !open {
		t.Fatal("publish closed the subscription")
	}
	hub.invalidate(userID)
	if _, open := <-first; open {
		t.Fatal("session invalidation did not close the subscription")
	}
}

func TestEventHubInvalidatesAllUsers(t *testing.T) {
	hub := newEventHub()
	first, unsubscribeFirst, ok := hub.subscribe(uuid.New(), 1)
	if !ok {
		t.Fatal("first subscription was rejected")
	}
	defer unsubscribeFirst()
	second, unsubscribeSecond, ok := hub.subscribe(uuid.New(), 1)
	if !ok {
		t.Fatal("second subscription was rejected")
	}
	defer unsubscribeSecond()

	hub.invalidateAll()
	for index, subscription := range []<-chan struct{}{first, second} {
		if _, open := <-subscription; open {
			t.Fatalf("subscription %d remained open", index)
		}
	}
}

func TestEffectiveSessionExpiryUsesEarliestBoundary(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		sess session
		want time.Time
	}{
		{
			name: "idle boundary",
			sess: session{ExpiresAt: now.Add(12 * time.Hour), IdleExpiresAt: now.Add(2 * time.Hour)},
			want: now.Add(2 * time.Hour),
		},
		{
			name: "hard boundary",
			sess: session{ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(2 * time.Hour)},
			want: now.Add(time.Hour),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := effectiveSessionExpiry(test.sess); !got.Equal(test.want) {
				t.Fatalf("expiry = %v, want %v", got, test.want)
			}
		})
	}
}
