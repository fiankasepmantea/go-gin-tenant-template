//go:build integration

package integration

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/idgen"
	"github.com/fiankasepmantea/go-gin-tenant-template/internal/platform/ratelimit"
)

// redisStore connects to TEST_REDIS_ADDR (skipped when unset). Keys are random, so tests never touch real data.
func redisStore(t *testing.T) (*ratelimit.RedisStore, *redis.Client) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("cannot reach Redis at %s: %v", addr, err)
	}
	return ratelimit.NewRedisStore(rdb), rdb
}

func TestRedisStore(t *testing.T) {
	s, rdb := redisStore(t)
	ctx := context.Background()
	key := "rl:test:" + idgen.New()
	t.Cleanup(func() { rdb.Del(ctx, key) })

	if n, ttl, err := s.Get(ctx, key); err != nil || n != 0 || ttl != 0 {
		t.Fatalf("missing key: %d %v %v", n, ttl, err)
	}
	for i := int64(1); i <= 3; i++ {
		n, ttl, err := s.Incr(ctx, key, 800*time.Millisecond)
		if err != nil || n != i || ttl <= 0 || ttl > 800*time.Millisecond {
			t.Fatalf("incr %d: %d %v %v", i, n, ttl, err)
		}
	}
	if n, ttl, err := s.Get(ctx, key); err != nil || n != 3 || ttl <= 0 {
		t.Fatalf("get: %d %v %v", n, ttl, err)
	}
	// the window is fixed: later increments do not extend it
	time.Sleep(900 * time.Millisecond)
	if n, _, err := s.Get(ctx, key); err != nil || n != 0 {
		t.Fatalf("the counter must expire with its window: %d %v", n, err)
	}
	if n, _, _ := s.Incr(ctx, key, time.Minute); n != 1 {
		t.Fatalf("a new window starts at 1, got %d", n)
	}
	if err := s.Del(ctx, key); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := s.Get(ctx, key); n != 0 {
		t.Fatalf("del: %d", n)
	}
}

func TestRedisStore_RepairsACounterWithoutExpiry(t *testing.T) {
	s, rdb := redisStore(t)
	ctx := context.Background()
	key := "rl:test:" + idgen.New()
	t.Cleanup(func() { rdb.Del(ctx, key) })

	if err := rdb.Set(ctx, key, 7, 0).Err(); err != nil { // a counter that would live forever
		t.Fatal(err)
	}
	n, ttl, err := s.Incr(ctx, key, time.Minute)
	if err != nil || n != 8 || ttl <= 0 {
		t.Fatalf("incr: %d %v %v", n, ttl, err)
	}
}

func TestRedisStore_ConcurrentIncrementsAreExact(t *testing.T) {
	s, rdb := redisStore(t)
	ctx := context.Background()
	key := "rl:test:" + idgen.New()
	t.Cleanup(func() { rdb.Del(ctx, key) })

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := s.Incr(ctx, key, time.Minute); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n, _, _ := s.Get(ctx, key); n != 50 {
		t.Fatalf("count = %d, want 50", n)
	}
}

func TestLimiter_OnRedis(t *testing.T) {
	s, _ := redisStore(t)
	ctx := context.Background()
	l := ratelimit.New(s, nil)
	checks := []ratelimit.Check{{Key: "it:" + idgen.New(), Limit: 2, Window: time.Minute}}

	l.Hit(ctx, checks)
	if _, blocked := l.Blocked(ctx, checks); blocked {
		t.Fatal("blocked too early")
	}
	l.Hit(ctx, checks)
	wait, blocked := l.Blocked(ctx, checks)
	if !blocked || wait <= 0 || wait > time.Minute {
		t.Fatalf("Blocked = %v %v", wait, blocked)
	}
	l.Reset(ctx, checks)
	if _, blocked := l.Blocked(ctx, checks); blocked {
		t.Fatal("reset must unblock")
	}
}

func TestLimiter_FailsOpenWhenRedisIsDown(t *testing.T) {
	// nothing listens on this port
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1})
	defer rdb.Close()
	l := ratelimit.New(ratelimit.NewRedisStore(rdb), nil)
	checks := []ratelimit.Check{{Key: "x", Limit: 1, Window: time.Minute}}
	l.Hit(context.Background(), checks)
	if _, blocked := l.Blocked(context.Background(), checks); blocked {
		t.Fatal("a Redis outage must not block logins")
	}
}
