package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newRPMReservationTestCache(t *testing.T) (*RPMCacheImpl, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &RPMCacheImpl{rdb: client}, client, server
}

func TestReserveRPMEnforcesLimitAtomically(t *testing.T) {
	cache, _, _ := newRPMReservationTestCache(t)
	const (
		accountID = int64(91)
		limit     = 7
		requests  = 100
	)

	type result struct {
		allowed bool
		cancel  func()
		err     error
	}
	results := make(chan result, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer workers.Done()
			allowed, cancel, err := cache.ReserveRPM(context.Background(), accountID, limit)
			results <- result{allowed: allowed, cancel: cancel, err: err}
		}()
	}
	workers.Wait()
	close(results)

	var allowedCount int
	for result := range results {
		require.NoError(t, result.err)
		if result.allowed {
			allowedCount++
			require.NotNil(t, result.cancel)
		} else {
			require.Nil(t, result.cancel)
		}
	}
	require.Equal(t, limit, allowedCount)

	count, err := cache.GetRPM(context.Background(), accountID)
	require.NoError(t, err)
	require.Equal(t, limit, count)
}

func TestReserveRPMDeniedRequestDoesNotIncrement(t *testing.T) {
	cache, _, _ := newRPMReservationTestCache(t)
	ctx := context.Background()

	allowed, cancel, err := cache.ReserveRPM(ctx, 92, 1)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NotNil(t, cancel)

	allowed, cancel, err = cache.ReserveRPM(ctx, 92, 1)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Nil(t, cancel)

	count, err := cache.GetRPM(ctx, 92)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestCancelRPMReservationIsIdempotentAndIndependentOfRequestContext(t *testing.T) {
	cache, _, server := newRPMReservationTestCache(t)
	requestCtx, stopRequest := context.WithCancel(context.Background())
	defer stopRequest()
	ctx := context.Background()

	allowed, cancelFirst, err := cache.ReserveRPM(requestCtx, 93, 0)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, cancelSecond, err := cache.ReserveRPM(ctx, 93, 0)
	require.NoError(t, err)
	require.True(t, allowed)

	key, err := cache.currentMinuteKey(ctx, 93)
	require.NoError(t, err)
	initialTTL := server.TTL(key)
	require.Equal(t, rpmKeyTTL, initialTTL)

	stopRequest()
	cancelFirst()
	cancelFirst()

	count, err := cache.GetRPM(ctx, 93)
	require.NoError(t, err)
	require.Equal(t, 1, count, "repeated cancellation must release only its own reservation once")
	require.Equal(t, initialTTL, server.TTL(key), "cancellation must preserve the counter TTL")

	cancelSecond()
	count, err = cache.GetRPM(ctx, 93)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestCancelRPMReservationAfterMinuteChangeKeepsNewMinuteCount(t *testing.T) {
	cache, client, server := newRPMReservationTestCache(t)
	ctx := context.Background()
	const accountID = int64(94)
	server.SetTime(time.Unix(1_800_000_000, 0).Truncate(time.Minute).Add(30 * time.Second))

	allowed, cancelOldMinute, err := cache.ReserveRPM(ctx, accountID, 1)
	require.NoError(t, err)
	require.True(t, allowed)
	oldKey, err := cache.currentMinuteKey(ctx, accountID)
	require.NoError(t, err)

	server.SetTime(time.Unix(1_800_000_000, 0).Truncate(time.Minute).Add(91 * time.Second))
	allowed, cancelNewMinute, err := cache.ReserveRPM(ctx, accountID, 1)
	require.NoError(t, err)
	require.True(t, allowed)
	newKey, err := cache.currentMinuteKey(ctx, accountID)
	require.NoError(t, err)
	require.NotEqual(t, oldKey, newKey)

	cancelOldMinute()

	newMinuteCount, err := cache.GetRPM(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, 1, newMinuteCount)
	oldMinuteCount, err := client.Get(ctx, oldKey).Int()
	require.NoError(t, err)
	require.Zero(t, oldMinuteCount)

	cancelNewMinute()
}

func TestReserveRPMNonpositiveLimitCountsWithoutDenial(t *testing.T) {
	cache, _, _ := newRPMReservationTestCache(t)
	ctx := context.Background()
	const accountID = int64(95)

	for _, limit := range []int{0, -1, 0} {
		allowed, cancel, err := cache.ReserveRPM(ctx, accountID, limit)
		require.NoError(t, err)
		require.True(t, allowed)
		require.NotNil(t, cancel)
	}

	count, err := cache.GetRPM(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, 3, count)
}

func TestReserveRPMReportsRedisFailure(t *testing.T) {
	cache, client, _ := newRPMReservationTestCache(t)
	require.NoError(t, client.Close())

	allowed, cancel, err := cache.ReserveRPM(context.Background(), 96, 1)
	require.Error(t, err)
	require.False(t, allowed)
	require.Nil(t, cancel)
}

func TestRPMReservationUsesAccountMinuteCounterKey(t *testing.T) {
	cache, client, _ := newRPMReservationTestCache(t)
	ctx := context.Background()
	const accountID = int64(97)

	allowed, cancel, err := cache.ReserveRPM(ctx, accountID, 0)
	require.NoError(t, err)
	require.True(t, allowed)
	key, err := cache.currentMinuteKey(ctx, accountID)
	require.NoError(t, err)
	value, err := client.Get(ctx, key).Int()
	require.NoError(t, err)
	require.Equal(t, 1, value)
	require.Regexp(t, fmt.Sprintf(`^rpm:%d:\d+$`, accountID), key)
	cancel()
}
