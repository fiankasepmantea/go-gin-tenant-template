package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTest() (*Limiter, *clock, *MemoryStore) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := NewMemoryStore(c.now)
	return New(s, nil), c, s
}

func TestLimiter_BlocksAtTheLimitAndReleasesWhenTheWindowEnds(t *testing.T) {
	l, clk, _ := newTest()
	ctx := context.Background()
	checks := []Check{{Key: "login:a", Limit: 3, Window: time.Minute}}

	for i := 0; i < 3; i++ {
		if _, blocked := l.Blocked(ctx, checks); blocked {
			t.Fatalf("blocked after %d events", i)
		}
		l.Hit(ctx, checks)
	}
	wait, blocked := l.Blocked(ctx, checks)
	if !blocked || wait <= 0 || wait > time.Minute {
		t.Fatalf("Blocked() = %v, %v", wait, blocked)
	}
	clk.advance(30 * time.Second)
	if wait2, _ := l.Blocked(ctx, checks); wait2 >= wait {
		t.Errorf("retry-after must shrink: %v then %v", wait, wait2)
	}
	clk.advance(31 * time.Second)
	if _, blocked := l.Blocked(ctx, checks); blocked {
		t.Fatal("must be released when the window ends")
	}
}

func TestLimiter_AnyCheckBlocks(t *testing.T) {
	l, _, _ := newTest()
	ctx := context.Background()
	strict := Check{Key: "k1", Limit: 1, Window: time.Minute}
	loose := Check{Key: "k2", Limit: 100, Window: time.Minute}
	l.Hit(ctx, []Check{strict, loose})
	if _, blocked := l.Blocked(ctx, []Check{loose, strict}); !blocked {
		t.Fatal("the strict check must block")
	}
	if _, blocked := l.Blocked(ctx, []Check{loose}); blocked {
		t.Fatal("the loose check alone must not block")
	}
}

func TestLimiter_ResetAndIndependentKeys(t *testing.T) {
	l, _, _ := newTest()
	ctx := context.Background()
	a := []Check{{Key: "a", Limit: 1, Window: time.Minute}}
	b := []Check{{Key: "b", Limit: 1, Window: time.Minute}}
	l.Hit(ctx, a)
	if _, blocked := l.Blocked(ctx, b); blocked {
		t.Fatal("keys must be independent")
	}
	l.Reset(ctx, a)
	if _, blocked := l.Blocked(ctx, a); blocked {
		t.Fatal("reset must clear the counter")
	}
}

func TestStoreKeyHidesTheKey(t *testing.T) {
	k := storeKey("login:alice@example.com|203.0.113.7")
	if strings.Contains(k, "alice") || strings.Contains(k, "203") || !strings.HasPrefix(k, "rl:") || len(k) != 3+32 {
		t.Errorf("storeKey = %q", k)
	}
	if storeKey("a") == storeKey("b") {
		t.Error("different keys must differ")
	}
}

type brokenStore struct{}

func (brokenStore) Incr(context.Context, string, time.Duration) (int64, time.Duration, error) {
	return 0, 0, errors.New("down")
}
func (brokenStore) Get(context.Context, string) (int64, time.Duration, error) {
	return 0, 0, errors.New("down")
}
func (brokenStore) Del(context.Context, ...string) error { return errors.New("down") }

func TestLimiter_FailsOpen(t *testing.T) {
	l := New(brokenStore{}, nil)
	ctx := context.Background()
	checks := []Check{{Key: "a", Limit: 1, Window: time.Minute}}
	l.Hit(ctx, checks)
	l.Reset(ctx, checks)
	if _, blocked := l.Blocked(ctx, checks); blocked {
		t.Fatal("a broken store must not block anyone")
	}
}

func TestMemoryStore_BoundedAndExpires(t *testing.T) {
	_, clk, s := newTest()
	ctx := context.Background()
	if _, _, err := s.Incr(ctx, "x", time.Second); err != nil {
		t.Fatal(err)
	}
	clk.advance(2 * time.Second)
	if n, _, _ := s.Get(ctx, "x"); n != 0 {
		t.Fatalf("expired counter = %d", n)
	}
	for i := 0; i < maxMemoryKeys; i++ {
		s.m[fmt.Sprintf("k%d", i)] = memEntry{count: 1, expires: clk.now().Add(time.Hour)}
	}
	if _, _, err := s.Incr(ctx, "overflow", time.Minute); err == nil {
		t.Fatal("a full store must refuse new keys")
	}
	if len(s.m) > maxMemoryKeys {
		t.Fatalf("store grew to %d", len(s.m))
	}
}
