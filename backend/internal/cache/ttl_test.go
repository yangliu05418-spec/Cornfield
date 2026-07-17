package cache

import (
	"testing"
	"time"
)

func TestTTLExpiryAndClear(t *testing.T) {
	cache := NewTTL[string, int](2)
	cache.Set("a", 1, time.Millisecond)
	time.Sleep(2 * time.Millisecond)
	if _, ok := cache.Get("a"); ok {
		t.Fatal("expired entry remained visible")
	}
	cache.Set("b", 2, time.Minute)
	cache.Clear()
	if _, ok := cache.Get("b"); ok {
		t.Fatal("clear retained an entry")
	}
}
