// Package ttlcache is a small in-process cache whose entries expire after a fixed time.
//
// It is meant for data that is read on every request and changes rarely (permissions, the endpoint list).
// Each process has its own cache, so a change made in another replica is seen after at most one TTL.
// Callers that change the data in the same process can call Delete or Clear to make it visible at once.
package ttlcache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value   V
	expires time.Time
}

// Cache is safe for concurrent use.
type Cache[K comparable, V any] struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu sync.Mutex
	m  map[K]entry[V]
}

// New creates a cache. ttl must be positive. max is the number of entries after which expired entries are
// removed, and if that is not enough the whole cache is cleared (a simple bound on memory, not an LRU).
func New[K comparable, V any](ttl time.Duration, max int) *Cache[K, V] {
	return newWithClock[K, V](ttl, max, time.Now)
}

func newWithClock[K comparable, V any](ttl time.Duration, max int, now func() time.Time) *Cache[K, V] {
	if ttl <= 0 {
		panic("ttlcache: ttl must be positive")
	}
	if max < 1 {
		max = 1
	}
	return &Cache[K, V]{ttl: ttl, max: max, now: now, m: make(map[K]entry[V])}
}

// Get returns the value if it is present and not expired.
func (c *Cache[K, V]) Get(k K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || !c.now().Before(e.expires) {
		delete(c.m, k)
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores a value for one TTL.
func (c *Cache[K, V]) Set(k K, v V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.m[k]; !exists && len(c.m) >= c.max {
		now := c.now()
		for key, e := range c.m {
			if !now.Before(e.expires) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= c.max {
			clear(c.m)
		}
	}
	c.m[k] = entry[V]{value: v, expires: c.now().Add(c.ttl)}
}

// Delete removes one key.
func (c *Cache[K, V]) Delete(k K) {
	c.mu.Lock()
	delete(c.m, k)
	c.mu.Unlock()
}

// Clear removes everything.
func (c *Cache[K, V]) Clear() {
	c.mu.Lock()
	clear(c.m)
	c.mu.Unlock()
}

// Len returns the number of stored entries, including expired ones that were not read yet.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
