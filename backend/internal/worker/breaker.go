package worker

import (
	"sync"
	"time"
)

type breakerSample struct {
	at      time.Time
	success bool
}

type breakerState struct {
	samples        []breakerSample
	openUntil      time.Time
	halfOpenActive bool
}

type Breaker struct {
	mu       sync.Mutex
	states   map[string]*breakerState
	window   time.Duration
	minimum  int
	ratio    float64
	cooldown time.Duration
}

func NewBreaker() *Breaker {
	return &Breaker{states: make(map[string]*breakerState), window: time.Minute, minimum: 10, ratio: .5, cooldown: 30 * time.Second}
}

func (b *Breaker) Allow(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.state(key)
	if state.openUntil.IsZero() {
		return true
	}
	if time.Now().Before(state.openUntil) || state.halfOpenActive {
		return false
	}
	state.halfOpenActive = true
	return true
}

// Abandon releases a half-open probe reservation when no provider request was
// made (for example because the database-side concurrency quota filled up).
func (b *Breaker) Abandon(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if state := b.states[key]; state != nil {
		state.halfOpenActive = false
	}
}

func (b *Breaker) Record(key string, success bool) (opened bool, until time.Time) {
	return b.RecordPolicy(key, success, b.minimum, b.ratio, b.cooldown)
}

// ForceOpen immediately gates new submissions after a provider authentication
// or quota failure. The durable provider pause remains authoritative; this
// in-memory gate closes the interval before that state is observed elsewhere.
func (b *Breaker) ForceOpen(key string, cooldown time.Duration) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cooldown <= 0 {
		cooldown = b.cooldown
	}
	state := b.state(key)
	state.halfOpenActive = false
	state.samples = nil
	state.openUntil = time.Now().Add(cooldown)
	return state.openUntil
}

func (b *Breaker) RecordPolicy(key string, success bool, minimum int, ratio float64, cooldown time.Duration) (opened bool, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	minimum, ratio, cooldown = b.normalizedPolicy(minimum, ratio, cooldown)
	return b.recordLocked(b.state(key), success, minimum, ratio, cooldown)
}

// RecordPassivePolicy records ordinary traffic without consuming a half-open
// probe. Existing paid jobs may keep polling while new submissions are gated.
func (b *Breaker) RecordPassivePolicy(key string, success bool, minimum int, ratio float64, cooldown time.Duration) (opened bool, until time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	minimum, ratio, cooldown = b.normalizedPolicy(minimum, ratio, cooldown)
	state := b.state(key)
	if !state.openUntil.IsZero() || state.halfOpenActive {
		return !state.openUntil.IsZero(), state.openUntil
	}
	return b.recordLocked(state, success, minimum, ratio, cooldown)
}

func (b *Breaker) normalizedPolicy(minimum int, ratio float64, cooldown time.Duration) (int, float64, time.Duration) {
	if minimum < 1 {
		minimum = b.minimum
	}
	if ratio <= 0 || ratio > 1 {
		ratio = b.ratio
	}
	if cooldown <= 0 {
		cooldown = b.cooldown
	}
	return minimum, ratio, cooldown
}

func (b *Breaker) recordLocked(state *breakerState, success bool, minimum int, ratio float64, cooldown time.Duration) (opened bool, until time.Time) {
	now := time.Now()
	if state.halfOpenActive {
		state.halfOpenActive = false
		if success {
			state.openUntil = time.Time{}
			state.samples = nil
			return false, time.Time{}
		}
		state.openUntil = now.Add(cooldown)
		return true, state.openUntil
	}
	cutoff := now.Add(-b.window)
	kept := state.samples[:0]
	for _, sample := range state.samples {
		if sample.at.After(cutoff) {
			kept = append(kept, sample)
		}
	}
	state.samples = append(kept, breakerSample{at: now, success: success})
	if len(state.samples) < minimum {
		return false, time.Time{}
	}
	failures := 0
	for _, sample := range state.samples {
		if !sample.success {
			failures++
		}
	}
	if float64(failures)/float64(len(state.samples)) >= ratio {
		state.openUntil = now.Add(cooldown)
		return true, state.openUntil
	}
	return false, time.Time{}
}

func (b *Breaker) state(key string) *breakerState {
	state := b.states[key]
	if state == nil {
		state = &breakerState{}
		b.states[key] = state
	}
	return state
}
