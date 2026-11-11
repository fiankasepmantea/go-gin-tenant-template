package ratelimit

import (
	"context"
	"sync"
	"time"
)

// maxMemoryKeys bounds the memory store. When it is full, expired counters are dropped first; if that is not
// enough, new keys are not counted (the limiter then allows, as for any store failure).
const maxMemoryKeys = 100_000

type memEntry struct {
	count   int64
	expires time.Time
}

// MemoryStore keeps counters in the process. It is correct for ONE replica only: with several replicas each
// has its own counters, so the effective limit is multiplied. Use Redis in production with more than one replica.
type MemoryStore struct {
	mu  sync.Mutex
	m   map[string]memEntry
	now func() time.Time
}

// NewMemoryStore creates an empty store. now may be nil (time.Now).
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{m: make(map[string]memEntry), now: now}
}

func (s *MemoryStore) live(key string) (memEntry, bool) {
	e, ok := s.m[key]
	if ok && !s.now().Before(e.expires) {
		delete(s.m, key)
		return memEntry{}, false
	}
	return e, ok
}

func (s *MemoryStore) Incr(_ context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.live(key)
	if !ok {
		if len(s.m) >= maxMemoryKeys {
			s.purge()
		}
		if len(s.m) >= maxMemoryKeys {
			return 0, 0, errMemoryFull
		}
		e = memEntry{expires: s.now().Add(window)}
	}
	e.count++
	s.m[key] = e
	return e.count, e.expires.Sub(s.now()), nil
}

func (s *MemoryStore) Get(_ context.Context, key string) (int64, time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.live(key)
	if !ok {
		return 0, 0, nil
	}
	return e.count, e.expires.Sub(s.now()), nil
}

func (s *MemoryStore) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		delete(s.m, k)
	}
	return nil
}

func (s *MemoryStore) purge() {
	now := s.now()
	for k, e := range s.m {
		if !now.Before(e.expires) {
			delete(s.m, k)
		}
	}
}

type memoryFullError struct{}

func (memoryFullError) Error() string { return "ratelimit: memory store is full" }

var errMemoryFull error = memoryFullError{}
