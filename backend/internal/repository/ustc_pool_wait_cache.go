package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const ustcPoolWaitKeyPrefix = "ustc:pool_wait:"

const ustcPoolWaitReleaseTimeout = 2 * time.Second

var _ service.USTCPoolWaitCache = (*RPMCacheImpl)(nil)

var reserveUSTCPoolWaitScript = redis.NewScript(`
local now = redis.call('TIME')
local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)

-- A retried command may already own the last permit. Confirm that lease
-- before checking capacity, without extending its original expiry.
if redis.call('ZSCORE', KEYS[1], ARGV[2]) then
  return 1
end

local limit = tonumber(ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= limit then
  return 0
end

local lease_until = now_ms + tonumber(ARGV[3])
redis.call('ZADD', KEYS[1], lease_until, ARGV[2])

-- Keep the key until the longest current lease expires. A shorter new lease
-- must never shorten the lifetime of another waiter's active permit.
local last = redis.call('ZREVRANGE', KEYS[1], 0, 0, 'WITHSCORES')
redis.call('PEXPIREAT', KEYS[1], tonumber(last[2]))
return 1
`)

var releaseUSTCPoolWaitScript = redis.NewScript(`
return redis.call('ZREM', KEYS[1], ARGV[1])
`)

// AcquireUSTCPoolWait reserves one lease from the shared per-pool waiting
// capacity. The Redis script uses Redis server time so all service instances
// agree on both expiry and admission.
func (c *RPMCacheImpl) AcquireUSTCPoolWait(ctx context.Context, poolKey string, maxWaiting int, ttl time.Duration) (bool, func(), error) {
	if ttl <= 0 {
		return false, nil, errors.New("ustc pool wait ttl must be positive")
	}
	if poolKey == "" {
		return false, nil, errors.New("ustc pool wait key must not be empty")
	}
	if maxWaiting <= 0 {
		return false, nil, nil
	}

	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return false, nil, fmt.Errorf("ustc pool wait token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes[:])
	key := ustcPoolWaitKeyPrefix + poolKey

	ttlMillis := int64(ttl / time.Millisecond)
	if ttl%time.Millisecond != 0 {
		ttlMillis++
	}
	allowed, err := reserveUSTCPoolWaitScript.Run(ctx, c.rdb, []string{key}, maxWaiting, token, ttlMillis).Int()
	if err != nil {
		return false, nil, fmt.Errorf("ustc pool wait acquire: %w", err)
	}
	if allowed == 0 {
		return false, nil, nil
	}

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), ustcPoolWaitReleaseTimeout)
			defer cancel()
			if err := releaseUSTCPoolWaitScript.Run(releaseCtx, c.rdb, []string{key}, token).Err(); err != nil {
				// Do not include the Redis key or lease token in logs.
				slog.Warn("ustc_pool_wait_release_failed")
			}
		})
	}
	return true, release, nil
}

// USTCHasWaiting is a conservative, read-only guard for low-priority speed
// probes. It includes pools on other backend instances and ignores expired
// leases using Redis time. A bounded scan fails closed if there are many pools.
func (c *RPMCacheImpl) USTCHasWaiting(ctx context.Context) (bool, error) {
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return false, err
	}
	var cursor uint64
	for page := 0; page < 16; page++ {
		keys, next, err := c.rdb.Scan(ctx, cursor, ustcPoolWaitKeyPrefix+"*", 64).Result()
		if err != nil {
			return false, err
		}
		if len(keys) > 0 {
			pipeline := c.rdb.Pipeline()
			counts := make([]*redis.IntCmd, 0, len(keys))
			for _, key := range keys {
				counts = append(counts, pipeline.ZCount(ctx, key, fmt.Sprint(now.UnixMilli()+1), "+inf"))
			}
			if _, err = pipeline.Exec(ctx); err != nil {
				return false, err
			}
			for _, count := range counts {
				if count.Val() > 0 {
					return true, nil
				}
			}
		}
		if next == 0 {
			return false, nil
		}
		cursor = next
	}
	return true, nil
}
