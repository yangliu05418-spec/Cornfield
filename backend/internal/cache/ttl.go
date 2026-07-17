package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

type TTL[K comparable, V any] struct {
	mu      sync.RWMutex
	items   map[K]entry[V]
	maxSize int
}

func NewTTL[K comparable, V any](maxSize int) *TTL[K, V] {
	return &TTL[K, V]{items: make(map[K]entry[V]), maxSize: maxSize}
}

func (c *TTL[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	item, ok := c.items[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(item.expiresAt) {
		if ok {
			c.Delete(key)
		}
		var zero V
		return zero, false
	}
	return item.value, true
}

func (c *TTL[K, V]) Set(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= c.maxSize {
		now := time.Now()
		for k, item := range c.items {
			if now.After(item.expiresAt) {
				delete(c.items, k)
			}
		}
	}
	if len(c.items) >= c.maxSize {
		for k := range c.items {
			delete(c.items, k)
			break
		}
	}
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(ttl)}
}

func (c *TTL[K, V]) Delete(key K) {
	c.mu.Lock()
	delete(c.items, key)
	c.mu.Unlock()
}

func (c *TTL[K, V]) DeleteWhere(match func(K, V) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, item := range c.items {
		if match(key, item.value) {
			delete(c.items, key)
		}
	}
}

func (c *TTL[K, V]) Clear() {
	c.mu.Lock()
	c.items = make(map[K]entry[V])
	c.mu.Unlock()
}
