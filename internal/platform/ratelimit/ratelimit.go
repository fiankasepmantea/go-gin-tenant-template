// Package ratelimit slows down guessing attacks (login, password reset) with fixed-window counters.
//
// A counter lives in a Store: Redis in production, memory for a single process (development, tests). The
// Limiter on top of it is deliberately forgiving: if the store fails, the request is allowed and the error is
// logged. A rate limiter is a second line of defense; an outage of Redis must not lock every user out.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"
)

// Store counts events per key in a fixed window.
type Store interface {
	// Incr adds one to the counter of key and returns the new count and the time left in the window.
	// The window starts at the first Incr and the counter disappears when it ends.
	Incr(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error)
	// Get returns the counter of key and the time left in the window. A missing key is (0, 0, nil).
	Get(ctx context.Context, key string) (count int64, ttl time.Duration, err error)
	// Del removes the counters.
	Del(ctx context.Context, keys ...string) error
}

// Check is one counter that a request is measured against.
type Check struct {
	// Key identifies what is counted, for example "login:ip:203.0.113.7". It is hashed before it reaches the
	// store, so usernames and addresses never appear in Redis.
	Key    string
	Limit  int           // the request is refused once the counter reached this value
	Window time.Duration // how long a counter lives
}

// Limiter applies Checks to a Store.
type Limiter struct {
	store Store
	log   *slog.Logger
}

// New creates a Limiter. log may be nil.
func New(store Store, log *slog.Logger) *Limiter {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Limiter{store: store, log: log}
}

// Blocked reports whether any check has reached its limit, and for how long it will stay so.
// On a store error it returns false (fail open).
func (l *Limiter) Blocked(ctx context.Context, checks []Check) (time.Duration, bool) {
	var longest time.Duration
	blocked := false
	for _, c := range checks {
		n, ttl, err := l.store.Get(ctx, storeKey(c.Key))
		if err != nil {
			l.log.Error("rate limiter unavailable, allowing the request", "error", err)
			return 0, false
		}
		if n >= int64(c.Limit) {
			blocked = true
			longest = max(longest, ttl)
		}
	}
	if blocked && longest <= 0 {
		longest = time.Second
	}
	return longest, blocked
}

// Hit counts one event on every check. Store errors are logged and ignored.
func (l *Limiter) Hit(ctx context.Context, checks []Check) {
	for _, c := range checks {
		if _, _, err := l.store.Incr(ctx, storeKey(c.Key), c.Window); err != nil {
			l.log.Error("rate limiter unavailable, event not counted", "error", err)
			return
		}
	}
}

// Reset forgets the counters of the checks (for example after a successful login).
func (l *Limiter) Reset(ctx context.Context, checks []Check) {
	keys := make([]string, 0, len(checks))
	for _, c := range checks {
		keys = append(keys, storeKey(c.Key))
	}
	if len(keys) == 0 {
		return
	}
	if err := l.store.Del(ctx, keys...); err != nil {
		l.log.Error("rate limiter unavailable, counters not reset", "error", err)
	}
}

// storeKey hashes the key: bounded length, no personal data in the store.
func storeKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "rl:" + hex.EncodeToString(sum[:16])
}
