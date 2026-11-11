package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// incrScript counts and sets the expiry in one step, so a crash between INCR and EXPIRE can never leave a
// counter that lives forever (which would block a user permanently).
var incrScript = redis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 or redis.call('PTTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return {c, redis.call('PTTL', KEYS[1])}
`)

// RedisStore keeps counters in Redis, shared by all replicas.
type RedisStore struct {
	rdb redis.Cmdable
}

// NewRedisStore creates a store on a Redis client (*redis.Client satisfies redis.Cmdable).
func NewRedisStore(rdb redis.Cmdable) *RedisStore { return &RedisStore{rdb: rdb} }

func (s *RedisStore) Incr(ctx context.Context, key string, window time.Duration) (int64, time.Duration, error) {
	res, err := incrScript.Run(ctx, s.rdb, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, 0, fmt.Errorf("ratelimit: incr: %w", err)
	}
	if len(res) != 2 {
		return 0, 0, fmt.Errorf("ratelimit: unexpected script result %v", res)
	}
	return res[0], time.Duration(max(res[1], 0)) * time.Millisecond, nil
}

func (s *RedisStore) Get(ctx context.Context, key string) (int64, time.Duration, error) {
	pipe := s.rdb.Pipeline()
	get := pipe.Get(ctx, key)
	ttl := pipe.PTTL(ctx, key)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil { //nolint:errorlint // redis.Nil is a sentinel returned as is
		return 0, 0, fmt.Errorf("ratelimit: get: %w", err)
	}
	n, err := get.Int64()
	if err == redis.Nil { //nolint:errorlint // sentinel
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("ratelimit: get: %w", err)
	}
	return n, max(ttl.Val(), 0), nil
}

func (s *RedisStore) Del(ctx context.Context, keys ...string) error {
	if err := s.rdb.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("ratelimit: del: %w", err)
	}
	return nil
}
