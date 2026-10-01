package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUSTCPoolWaitTestCache(t *testing.T) (*RPMCacheImpl, *redis.Client, *miniredis.Miniredis) {
	t.Helper()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &RPMCacheImpl{rdb: client}, client, server
}

func TestUSTCPoolWaitEnforcesSharedLimitAtomically(t *testing.T) {
	cache, client, _ := newUSTCPoolWaitTestCache(t)
	const (
		poolKey  = "group-7"
		limit    = 7
		requests = 100
	)

	type result struct {
		allowed bool
		release func()
		err     error
	}
	results := make(chan result, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer workers.Done()
			allowed, release, err := cache.AcquireUSTCPoolWait(context.Background(), poolKey, limit, time.Minute)
			results <- result{allowed: allowed, release: release, err: err}
		}()
	}
	workers.Wait()
	close(results)

	var releases []func()
	var allowedCount int
	for result := range results {
		require.NoError(t, result.err)
		if result.allowed {
			allowedCount++
			require.NotNil(t, result.release)
			releases = append(releases, result.release)
		} else {
			require.Nil(t, result.release)
		}
	}
	require.Equal(t, limit, allowedCount)
	count, err := client.ZCard(context.Background(), ustcPoolWaitKeyPrefix+poolKey).Result()
	require.NoError(t, err)
	require.EqualValues(t, limit, count)

	for _, release := range releases {
		release()
	}
	count, err = client.ZCard(context.Background(), ustcPoolWaitKeyPrefix+poolKey).Result()
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestUSTCPoolWaitReleaseIsIdempotentAndIgnoresRequestCancellation(t *testing.T) {
	cache, client, _ := newUSTCPoolWaitTestCache(t)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()

	allowed, releaseFirst, err := cache.AcquireUSTCPoolWait(requestCtx, "group-8", 2, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, releaseSecond, err := cache.AcquireUSTCPoolWait(context.Background(), "group-8", 2, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)

	cancelRequest()
	releaseFirst()
	releaseFirst()
	count, err := client.ZCard(context.Background(), ustcPoolWaitKeyPrefix+"group-8").Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "repeated release removes only its own lease once")

	releaseSecond()
	count, err = client.ZCard(context.Background(), ustcPoolWaitKeyPrefix+"group-8").Result()
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestUSTCPoolWaitReclaimsExpiredLeases(t *testing.T) {
	cache, client, server := newUSTCPoolWaitTestCache(t)
	ctx := context.Background()
	const poolKey = "group-expiry"

	allowed, oldRelease, err := cache.AcquireUSTCPoolWait(ctx, poolKey, 1, time.Second)
	require.NoError(t, err)
	require.True(t, allowed)
	server.FastForward(2 * time.Second)

	allowed, newRelease, err := cache.AcquireUSTCPoolWait(ctx, poolKey, 1, time.Second)
	require.NoError(t, err)
	require.True(t, allowed, "an expired lease must not block a new permit")
	oldRelease()
	count, err := client.ZCard(ctx, ustcPoolWaitKeyPrefix+poolKey).Result()
	require.NoError(t, err)
	require.EqualValues(t, 1, count, "releasing an expired token must preserve the new lease")
	newRelease()
}

func TestUSTCPoolWaitOldReleaseDoesNotRemoveAReacquiredLease(t *testing.T) {
	cache, client, server := newUSTCPoolWaitTestCache(t)
	ctx := context.Background()
	const poolKey = "group-old-release"

	allowed, oldRelease, err := cache.AcquireUSTCPoolWait(ctx, poolKey, 2, time.Second)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, activeRelease, err := cache.AcquireUSTCPoolWait(ctx, poolKey, 2, 10*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)
	serverTime, err := client.Time(ctx).Result()
	require.NoError(t, err)
	server.SetTime(serverTime.Add(2 * time.Second))
	allowed, newRelease, err := cache.AcquireUSTCPoolWait(ctx, poolKey, 2, 10*time.Second)
	require.NoError(t, err)
	require.True(t, allowed)
	oldRelease()

	count, err := client.ZCard(ctx, ustcPoolWaitKeyPrefix+poolKey).Result()
	require.NoError(t, err)
	require.EqualValues(t, 2, count, "old token release must not consume either live permit")
	activeRelease()
	newRelease()
}

func TestUSTCPoolWaitReportsRedisFailure(t *testing.T) {
	cache, client, _ := newUSTCPoolWaitTestCache(t)
	require.NoError(t, client.Close())

	allowed, release, err := cache.AcquireUSTCPoolWait(context.Background(), "group-error", 1, time.Minute)
	require.Error(t, err)
	require.False(t, allowed)
	require.Nil(t, release)
}

func TestUSTCPoolWaitValidatesTTLAndNonpositiveLimit(t *testing.T) {
	cache, _, _ := newUSTCPoolWaitTestCache(t)

	allowed, release, err := cache.AcquireUSTCPoolWait(context.Background(), "group-invalid", 1, 0)
	require.Error(t, err)
	require.False(t, allowed)
	require.Nil(t, release)

	allowed, release, err = cache.AcquireUSTCPoolWait(context.Background(), "group-disabled", 0, time.Minute)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Nil(t, release)
}
