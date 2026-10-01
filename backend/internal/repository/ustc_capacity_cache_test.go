package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUSTCCapacityTestCache(t *testing.T) (*RPMCacheImpl, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &RPMCacheImpl{rdb: client}, client, server
}

func newUSTCCapacityCacheOnServer(t *testing.T, server *miniredis.Miniredis) *RPMCacheImpl {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &RPMCacheImpl{rdb: client}
}

func advanceUSTCRedisTime(t *testing.T, cache *RPMCacheImpl, server *miniredis.Miniredis, duration time.Duration) {
	t.Helper()
	now, err := cache.rdb.Time(context.Background()).Result()
	require.NoError(t, err)
	server.SetTime(now.Add(duration))
}

func primeUSTCCapacity(t *testing.T, cache *RPMCacheImpl, scope string, limits service.USTCLimits) {
	t.Helper()
	ctx := context.Background()
	ticket, _, err := cache.USTCReserve(ctx, scope, limits)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	require.True(t, ticket.Probe)
	require.True(t, ticket.Cold)
	committed, err := cache.USTCCommit(ctx, ticket, limits)
	require.NoError(t, err)
	require.True(t, committed)
	remaining := limits.RPM - 1
	limit := limits.RPM
	err = cache.USTCObserve(ctx, ticket, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &remaining,
		RPMLimit:   &limit,
	})
	require.NoError(t, err)
	require.NoError(t, cache.USTCRelease(ctx, ticket))
}

func TestUSTCCapacityColdReadIsVirtualAndReserveCreatesSingleProbe(t *testing.T) {
	cache, client, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 20, Parallel: 4}

	capacity, err := cache.USTCRead(ctx, "cold-key", limits)
	require.NoError(t, err)
	require.Equal(t, "verify_one", capacity.State)
	require.Zero(t, capacity.InFlight)
	require.Zero(t, capacity.Pending)
	require.Equal(t, 1, capacity.Available, "a cold key advertises at most its single verification slot")

	keys, err := client.Keys(ctx, "ustc:capacity:*").Result()
	require.NoError(t, err)
	require.Empty(t, keys, "a cold read must not persist state")

	first, reserved, err := cache.USTCReserve(ctx, "cold-key", limits)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.True(t, first.Probe)
	require.True(t, first.Cold)
	require.Equal(t, 1, reserved.InFlight)
	require.Equal(t, 1, reserved.Pending)
	require.Zero(t, reserved.Available, "the probe consumes the only verification slot")

	second, secondCapacity, err := cache.USTCReserve(ctx, "cold-key", limits)
	require.NoError(t, err)
	require.Nil(t, second, "only one probe may be reserved")
	require.Equal(t, 1, secondCapacity.InFlight)
	require.Equal(t, 1, secondCapacity.Pending)
	require.NoError(t, cache.USTCRelease(ctx, first))
}

func TestUSTCCapacityUnknownRedisStateRequiresOneProbe(t *testing.T) {
	cache, client, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 5}
	keys, err := cache.ustcCapacityKeys("unknown-state")
	require.NoError(t, err)
	require.NoError(t, client.HSet(ctx, keys[0], map[string]interface{}{
		"state":    "future_state",
		"epoch":    7,
		"used":     5,
		"rpm":      5,
		"parallel": 0,
		"active":   1,
		"deadline": 9_999_999_999_999,
	}).Err())

	capacity, err := cache.USTCRead(ctx, "unknown-state", limits)
	require.NoError(t, err)
	require.Equal(t, "verify_one", capacity.State)
	require.Zero(t, capacity.Used)

	probe, _, err := cache.USTCReserve(ctx, "unknown-state", limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe)
	require.True(t, probe.Cold)
	require.NoError(t, cache.USTCRelease(ctx, probe))
}

func TestUSTCCapacityAtomicallyEnforcesLimitAcrossInstances(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	other := newUSTCCapacityCacheOnServer(t, server)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 20}
	primeUSTCCapacity(t, cache, "shared", limits)

	const requests = 100
	type result struct {
		ticket *service.USTCTicket
		err    error
	}
	results := make(chan result, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for i := 0; i < requests; i++ {
		instance := cache
		if i%2 == 1 {
			instance = other
		}
		go func(instance *RPMCacheImpl) {
			defer workers.Done()
			ticket, _, err := instance.USTCReserve(ctx, "shared", limits)
			results <- result{ticket: ticket, err: err}
		}(instance)
	}
	workers.Wait()
	close(results)

	allowed := make([]*service.USTCTicket, 0, 19)
	for result := range results {
		require.NoError(t, result.err)
		if result.ticket != nil {
			require.False(t, result.ticket.Probe)
			allowed = append(allowed, result.ticket)
		}
	}
	require.Len(t, allowed, 19, "the first probe used one of the twenty RPM slots")

	capacity, err := other.USTCRead(ctx, "shared", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used)
	require.Equal(t, 19, capacity.Pending)
	require.Equal(t, 19, capacity.InFlight)
	require.Zero(t, capacity.Available)
	for _, ticket := range allowed {
		require.NoError(t, cache.USTCRelease(ctx, ticket))
	}
}

func TestUSTCCapacityWindowRunsSixtySecondsAcrossNaturalMinute(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 11, 59, 40, 0, time.UTC))
	limits := service.USTCLimits{RPM: 1}
	primeUSTCCapacity(t, cache, "natural-minute", limits)

	advanceUSTCRedisTime(t, cache, server, 20*time.Second)
	ticket, capacity, err := cache.USTCReserve(ctx, "natural-minute", limits)
	require.NoError(t, err)
	require.Nil(t, ticket, "crossing noon does not reset a fixed sixty-second window")
	require.Equal(t, "ready", capacity.State)
	require.Equal(t, 1, capacity.Used)

	advanceUSTCRedisTime(t, cache, server, 41*time.Second)
	ticket, capacity, err = cache.USTCReserve(ctx, "natural-minute", limits)
	require.NoError(t, err)
	require.NotNil(t, ticket, "capacity after natural-minute rollover: %+v", capacity)
	require.False(t, ticket.Probe, "a mature fixed window needs no extra probe after its full sixty-second wait")
	require.Equal(t, "ready", capacity.State)
	require.Zero(t, capacity.Used)
	committed, err := cache.USTCCommit(ctx, ticket, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCRelease(ctx, ticket))
}

func TestUSTCCapacity429WaitsThenAdmitsExactlyOneProbe(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 10}
	primeUSTCCapacity(t, cache, "retry-key", limits)

	ticket, _, err := cache.USTCReserve(ctx, "retry-key", limits)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	require.False(t, ticket.Probe)
	committed, err := cache.USTCCommit(ctx, ticket, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, ticket, service.USTCFeedback{
		StatusCode: 429,
		RetryAfter: 90 * time.Second,
	}))
	require.NoError(t, cache.USTCRelease(ctx, ticket))

	advanceUSTCRedisTime(t, cache, server, 89*time.Second)
	blocked, capacity, err := cache.USTCReserve(ctx, "retry-key", limits)
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, "sync_wait", capacity.State)

	advanceUSTCRedisTime(t, cache, server, 2*time.Second)
	const requests = 100
	type result struct {
		ticket *service.USTCTicket
		err    error
	}
	results := make(chan result, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for i := 0; i < requests; i++ {
		go func() {
			defer workers.Done()
			reserved, _, reserveErr := cache.USTCReserve(ctx, "retry-key", limits)
			results <- result{ticket: reserved, err: reserveErr}
		}()
	}
	workers.Wait()
	close(results)
	var probe *service.USTCTicket
	for result := range results {
		require.NoError(t, result.err)
		if result.ticket != nil {
			probe = result.ticket
		}
	}
	require.NotNil(t, probe, "capacity after Retry-After: %+v", capacity)
	require.True(t, probe.Probe, "capacity after old window: %+v", capacity)

	committed, err = cache.USTCCommit(ctx, probe, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, probe, service.USTCFeedback{StatusCode: 200}))
	capacity, err = cache.USTCRead(ctx, "retry-key", limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State, "a headerless probe after the full wait verifies recovery")
	require.Equal(t, 1, capacity.Used)
	require.NoError(t, cache.USTCRelease(ctx, probe))
}

func TestUSTCCapacityColdHeaderlessProbeWaitsAndNeedsResponseToRecover(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5}
	probe, _, err := cache.USTCReserve(ctx, "cold-sse", limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe)
	require.True(t, probe.Cold)
	committed, err := cache.USTCCommit(ctx, probe, limits)
	require.NoError(t, err)
	require.True(t, committed)
	receipt, err := cache.rdb.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, cache.USTCObserve(ctx, probe, service.USTCFeedback{StatusCode: 200}))
	capacity, err := cache.USTCRead(ctx, "cold-sse", limits)
	require.NoError(t, err)
	require.Equal(t, "sync_wait", capacity.State)
	require.Zero(t, capacity.Available)
	require.Equal(t, receipt.Add(60*time.Second).UnixMilli(), capacity.ResetAt.UnixMilli())
	require.NoError(t, cache.USTCRelease(ctx, probe))

	blocked, _, err := cache.USTCReserve(ctx, "cold-sse", limits)
	require.NoError(t, err)
	require.Nil(t, blocked)
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)
	recovery, _, err := cache.USTCReserve(ctx, "cold-sse", limits)
	require.NoError(t, err)
	require.NotNil(t, recovery)
	require.True(t, recovery.Probe)
	require.False(t, recovery.Cold, "the full conservative wait made this a recovery probe")
	committed, err = cache.USTCCommit(ctx, recovery, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, recovery, service.USTCFeedback{StatusCode: 200}))
	capacity, err = cache.USTCRead(ctx, "cold-sse", limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State)
	require.NoError(t, cache.USTCRelease(ctx, recovery))
}

func TestUSTCCapacityCooldownOnlyExtendsSharedDeadline(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5}
	primeUSTCCapacity(t, cache, "shared-cooldown", limits)
	require.NoError(t, cache.USTCCooldown(ctx, "shared-cooldown", 120*time.Second))
	started, err := cache.rdb.Time(ctx).Result()
	require.NoError(t, err)
	capacity, err := cache.USTCRead(ctx, "shared-cooldown", limits)
	require.NoError(t, err)
	require.Equal(t, "sync_wait", capacity.State)
	require.Equal(t, started.Add(120*time.Second).UnixMilli(), capacity.ResetAt.UnixMilli())
	require.NoError(t, cache.USTCCooldown(ctx, "shared-cooldown", 30*time.Second))
	capacity, err = cache.USTCRead(ctx, "shared-cooldown", limits)
	require.NoError(t, err)
	require.Equal(t, started.Add(120*time.Second).UnixMilli(), capacity.ResetAt.UnixMilli(), "a shorter shared cooldown cannot reduce the deadline")

	advanceUSTCRedisTime(t, cache, server, 121*time.Second)
	probe, _, err := cache.USTCReserve(ctx, "shared-cooldown", limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe)
	require.False(t, probe.Cold)
	require.NoError(t, cache.USTCRelease(ctx, probe))
}

func TestUSTCCapacity403ProbeRefundStaysInVerifyOne(t *testing.T) {
	cache, _, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 5}

	probe, _, err := cache.USTCReserve(ctx, "cold-denial", limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe)
	require.True(t, probe.Cold)
	committed, err := cache.USTCCommit(ctx, probe, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, probe, service.USTCFeedback{StatusCode: 403, Refund: true}))

	capacity, err := cache.USTCRead(ctx, "cold-denial", limits)
	require.NoError(t, err)
	require.Equal(t, "verify_one", capacity.State)
	require.Zero(t, capacity.Used)
	require.NoError(t, cache.USTCRelease(ctx, probe))

	next, capacity, err := cache.USTCReserve(ctx, "cold-denial", limits)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.True(t, next.Probe, "the denied cold probe did not verify a usable RPM window")
	require.True(t, next.Cold)
	require.Zero(t, capacity.Available)
	require.NoError(t, cache.USTCRelease(ctx, next))
}

func TestUSTCCapacityPendingReservationCanCommitAcrossMatureWindow(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 5}
	primeUSTCCapacity(t, cache, "pending-rollover", limits)

	pending, _, err := cache.USTCReserve(ctx, "pending-rollover", limits)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.False(t, pending.Probe)
	oldEpoch := pending.Epoch
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	capacity, err := cache.USTCRead(ctx, "pending-rollover", limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State)
	require.Zero(t, capacity.Used)
	require.Equal(t, 1, capacity.Pending, "pending RPM capacity carries across the fixed window")

	committed, err := cache.USTCCommit(ctx, pending, limits)
	require.NoError(t, err)
	require.True(t, committed, "a pending reservation can revalidate into the mature next window")
	require.NotEqual(t, oldEpoch, pending.Epoch)
	capacity, err = cache.USTCRead(ctx, "pending-rollover", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used)
	require.Zero(t, capacity.Pending)
	require.NoError(t, cache.USTCRelease(ctx, pending))
}

func TestUSTCCapacityOnlyFirstResponseMovesDeadlineAndHeaderCanTightenLimit(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 10}
	primeUSTCCapacity(t, cache, "response-deadline", limits)
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	first, _, err := cache.USTCReserve(ctx, "response-deadline", limits)
	require.NoError(t, err)
	require.NotNil(t, first)
	require.False(t, first.Probe)
	committed, err := cache.USTCCommit(ctx, first, limits)
	require.NoError(t, err)
	require.True(t, committed)
	advanceUSTCRedisTime(t, cache, server, 30*time.Second)
	firstReceipt, err := cache.rdb.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, cache.USTCObserve(ctx, first, service.USTCFeedback{StatusCode: 200}))

	second, _, err := cache.USTCReserve(ctx, "response-deadline", limits)
	require.NoError(t, err)
	require.NotNil(t, second)
	committed, err = cache.USTCCommit(ctx, second, limits)
	require.NoError(t, err)
	require.True(t, committed)
	advanceUSTCRedisTime(t, cache, server, 20*time.Second)
	secondReceipt, err := cache.rdb.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, cache.USTCObserve(ctx, second, service.USTCFeedback{StatusCode: 200}))

	capacity, err := cache.USTCRead(ctx, "response-deadline", limits)
	require.NoError(t, err)
	require.Equal(t, firstReceipt.Add(60*time.Second).UnixMilli(), capacity.ResetAt.UnixMilli(), "only the first request response extends its send-time deadline")
	require.Greater(t, capacity.ResetAt.UnixMilli(), secondReceipt.UnixMilli())
	require.NoError(t, cache.USTCRelease(ctx, first))
	require.NoError(t, cache.USTCRelease(ctx, second))
}

func TestUSTCCapacityRefundOfFirstMatureCommitClearsEmptyWindow(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5}
	primeUSTCCapacity(t, cache, "empty-window-refund", limits)
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	denied, _, err := cache.USTCReserve(ctx, "empty-window-refund", limits)
	require.NoError(t, err)
	require.NotNil(t, denied)
	require.False(t, denied.Probe, "a mature window admits the normal request")
	committed, err := cache.USTCCommit(ctx, denied, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, denied, service.USTCFeedback{StatusCode: 403, Refund: true}))
	require.NoError(t, cache.USTCRelease(ctx, denied))

	capacity, err := cache.USTCRead(ctx, "empty-window-refund", limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State)
	require.Zero(t, capacity.Used)
	require.True(t, capacity.ResetAt.IsZero(), "a pre-RPM denial must not anchor an empty mature window")
	require.Equal(t, limits.RPM, capacity.Available)

	next, _, err := cache.USTCReserve(ctx, "empty-window-refund", limits)
	require.NoError(t, err)
	require.NotNil(t, next, "the next real request can start a fresh window")
	committed, err = cache.USTCCommit(ctx, next, limits)
	require.NoError(t, err)
	require.True(t, committed)
	capacity, err = cache.USTCRead(ctx, "empty-window-refund", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used)
	require.False(t, capacity.ResetAt.IsZero(), "the next sent request starts a fresh deadline")
	require.NoError(t, cache.USTCRelease(ctx, next))
}

func TestUSTCCapacityRefundKeepsAnchorForNextCommittedResponse(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5}
	primeUSTCCapacity(t, cache, "surviving-window-refund", limits)
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	denied, _, err := cache.USTCReserve(ctx, "surviving-window-refund", limits)
	require.NoError(t, err)
	require.NotNil(t, denied)
	survivor, _, err := cache.USTCReserve(ctx, "surviving-window-refund", limits)
	require.NoError(t, err)
	require.NotNil(t, survivor)
	for _, ticket := range []*service.USTCTicket{denied, survivor} {
		committed, commitErr := cache.USTCCommit(ctx, ticket, limits)
		require.NoError(t, commitErr)
		require.True(t, committed)
	}

	require.NoError(t, cache.USTCObserve(ctx, denied, service.USTCFeedback{StatusCode: 403, Refund: true}))
	require.NoError(t, cache.USTCRelease(ctx, denied))
	advanceUSTCRedisTime(t, cache, server, 70*time.Second)
	capacity, err := cache.USTCRead(ctx, "surviving-window-refund", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used, "the other committed request still counts")
	require.True(t, capacity.ResetAt.IsZero(), "an unconfirmed surviving submission has no response-anchored deadline yet")

	receipt, err := cache.rdb.Time(ctx).Result()
	require.NoError(t, err)
	require.NoError(t, cache.USTCObserve(ctx, survivor, service.USTCFeedback{StatusCode: 200}))
	capacity, err = cache.USTCRead(ctx, "surviving-window-refund", limits)
	require.NoError(t, err)
	require.Equal(t, receipt.Add(60*time.Second).UnixMilli(), capacity.ResetAt.UnixMilli(), "the first non-refunded response anchors from receipt time")
	require.NoError(t, cache.USTCRelease(ctx, survivor))
}

func TestUSTCCapacityResponseLimitOnlyTightensCurrentWindow(t *testing.T) {
	cache, _, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 10}
	primeUSTCCapacity(t, cache, "provider-limit", limits)

	first, _, err := cache.USTCReserve(ctx, "provider-limit", limits)
	require.NoError(t, err)
	second, _, err := cache.USTCReserve(ctx, "provider-limit", limits)
	require.NoError(t, err)
	for _, ticket := range []*service.USTCTicket{first, second} {
		committed, commitErr := cache.USTCCommit(ctx, ticket, limits)
		require.NoError(t, commitErr)
		require.True(t, committed)
	}
	providerLimit, providerRemaining := 6, 3
	require.NoError(t, cache.USTCObserve(ctx, first, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &providerRemaining,
		RPMLimit:   &providerLimit,
	}))
	capacity, err := cache.USTCRead(ctx, "provider-limit", limits)
	require.NoError(t, err)
	require.NotNil(t, capacity.RPMLimit)
	require.Equal(t, 6, *capacity.RPMLimit)
	require.Equal(t, 4, capacity.Used, "provider usage plus the later committed request remains accounted")

	largerProviderLimit, highRemaining := 100, 99
	require.NoError(t, cache.USTCObserve(ctx, second, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &highRemaining,
		RPMLimit:   &largerProviderLimit,
	}))
	capacity, err = cache.USTCRead(ctx, "provider-limit", limits)
	require.NoError(t, err)
	require.Equal(t, 6, *capacity.RPMLimit, "a larger response header cannot raise the effective cap mid-window")
	require.Equal(t, 4, capacity.Used)
	require.NoError(t, cache.USTCRelease(ctx, first))
	require.NoError(t, cache.USTCRelease(ctx, second))
}

func TestUSTCCapacityLateSuccessDoesNotRecountRefundedLaterCommit(t *testing.T) {
	cache, _, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 10}
	primeUSTCCapacity(t, cache, "refund-order", limits)

	earlier, _, err := cache.USTCReserve(ctx, "refund-order", limits)
	require.NoError(t, err)
	later, _, err := cache.USTCReserve(ctx, "refund-order", limits)
	require.NoError(t, err)
	require.NotNil(t, earlier)
	require.NotNil(t, later)
	for _, ticket := range []*service.USTCTicket{earlier, later} {
		committed, commitErr := cache.USTCCommit(ctx, ticket, limits)
		require.NoError(t, commitErr)
		require.True(t, committed)
	}

	require.NoError(t, cache.USTCObserve(ctx, later, service.USTCFeedback{StatusCode: 403, Refund: true}))
	capacity, err := cache.USTCRead(ctx, "refund-order", limits)
	require.NoError(t, err)
	require.Equal(t, 2, capacity.Used, "the explicit pre-RPM denial refunds its own committed slot")

	providerLimit, earlierRemaining := 10, 8
	require.NoError(t, cache.USTCObserve(ctx, earlier, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &earlierRemaining,
		RPMLimit:   &providerLimit,
	}))
	capacity, err = cache.USTCRead(ctx, "refund-order", limits)
	require.NoError(t, err)
	require.Equal(t, 2, capacity.Used, "later_commits excludes the refunded request")
	require.NoError(t, cache.USTCRelease(ctx, earlier))
	require.NoError(t, cache.USTCRelease(ctx, later))
}

func TestUSTCCapacityResponsesOnlyTightenCountAndOldEpochRefundIsIgnored(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 10}
	primeUSTCCapacity(t, cache, "ordered-feedback", limits)

	older, _, err := cache.USTCReserve(ctx, "ordered-feedback", limits)
	require.NoError(t, err)
	newer, _, err := cache.USTCReserve(ctx, "ordered-feedback", limits)
	require.NoError(t, err)
	require.NotNil(t, older)
	require.NotNil(t, newer)
	for _, ticket := range []*service.USTCTicket{older, newer} {
		committed, commitErr := cache.USTCCommit(ctx, ticket, limits)
		require.NoError(t, commitErr)
		require.True(t, committed)
	}
	partialRemaining, providerLimit := 5, 10
	require.NoError(t, cache.USTCObserve(ctx, newer, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &partialRemaining,
		RPMLimit:   &providerLimit,
	}))
	staleRemaining := 9
	require.NoError(t, cache.USTCObserve(ctx, older, service.USTCFeedback{
		StatusCode: 200,
		Remaining:  &staleRemaining,
		RPMLimit:   &providerLimit,
	}))
	capacity, err := cache.USTCRead(ctx, "ordered-feedback", limits)
	require.NoError(t, err)
	require.Equal(t, 5, capacity.Used, "late high-remaining response cannot lower the observed count")

	advanceUSTCRedisTime(t, cache, server, 61*time.Second)
	newEpochTicket, _, err := cache.USTCReserve(ctx, "ordered-feedback", limits)
	require.NoError(t, err)
	require.NotNil(t, newEpochTicket)
	require.False(t, newEpochTicket.Probe)
	committed, err := cache.USTCCommit(ctx, newEpochTicket, limits)
	require.NoError(t, err)
	require.True(t, committed)

	// Both a late refund and a late 429 belong to the previous window.
	require.NoError(t, cache.USTCObserve(ctx, older, service.USTCFeedback{StatusCode: 403, Refund: true}))
	require.NoError(t, cache.USTCObserve(ctx, newer, service.USTCFeedback{StatusCode: 429, RetryAfter: time.Hour}))
	capacity, err = cache.USTCRead(ctx, "ordered-feedback", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used)
	require.Equal(t, "ready", capacity.State)

	for _, oldTicket := range []*service.USTCTicket{older, newer} {
		require.NoError(t, cache.USTCRelease(ctx, oldTicket))
	}
	require.NoError(t, cache.USTCRelease(ctx, newEpochTicket))
}

func TestUSTCCapacityLongStreamLeaseSurvivesWindowRollover(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 3, Parallel: 1}
	primeUSTCCapacity(t, cache, "long-stream", limits)

	stream, _, err := cache.USTCReserve(ctx, "long-stream", limits)
	require.NoError(t, err)
	require.NotNil(t, stream)
	require.False(t, stream.Probe)
	committed, err := cache.USTCCommit(ctx, stream, limits)
	require.NoError(t, err)
	require.True(t, committed)

	advanceUSTCRedisTime(t, cache, server, 61*time.Second)
	capacity, err := cache.USTCRead(ctx, "long-stream", limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State, "capacity after stream window rollover: %+v", capacity)
	require.Equal(t, 1, capacity.InFlight, "window reset must preserve an active stream lease")
	require.Zero(t, capacity.Used)

	blocked, capacity, err := cache.USTCReserve(ctx, "long-stream", limits)
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.Equal(t, 1, capacity.InFlight)
	require.NoError(t, cache.USTCRenew(ctx, stream))
	advanceUSTCRedisTime(t, cache, server, 90*time.Second)
	capacity, err = cache.USTCRead(ctx, "long-stream", limits)
	require.NoError(t, err)
	require.Equal(t, 1, capacity.InFlight, "renewal extends the stream beyond its original lease")

	require.NoError(t, cache.USTCRelease(ctx, stream))
	nextRequest, capacity, err := cache.USTCReserve(ctx, "long-stream", limits)
	require.NoError(t, err)
	require.NotNil(t, nextRequest)
	require.False(t, nextRequest.Probe)
	require.Equal(t, 1, capacity.InFlight)
	require.NoError(t, cache.USTCRelease(ctx, nextRequest))
}

func TestUSTCCapacityVerifyOneRetainsOneSlotBesidePreviousWindowStream(t *testing.T) {
	cache, _, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5, Parallel: 2}
	primeUSTCCapacity(t, cache, "recovery-with-stream", limits)

	stream, _, err := cache.USTCReserve(ctx, "recovery-with-stream", limits)
	require.NoError(t, err)
	require.NotNil(t, stream)
	committed, err := cache.USTCCommit(ctx, stream, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCCooldown(ctx, "recovery-with-stream", time.Minute))
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	capacity, err := cache.USTCRead(ctx, "recovery-with-stream", limits)
	require.NoError(t, err)
	require.Equal(t, "verify_one", capacity.State)
	require.Equal(t, 1, capacity.InFlight, "the previous-window stream retains its lease")
	require.Equal(t, 1, capacity.Available, "one free parallel slot remains for the recovery probe")

	probe, afterReserve, err := cache.USTCReserve(ctx, "recovery-with-stream", limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe)
	require.Equal(t, 2, afterReserve.InFlight)
	require.Zero(t, afterReserve.Available)
	require.NoError(t, cache.USTCRelease(ctx, probe))

	capacity, err = cache.USTCRead(ctx, "recovery-with-stream", limits)
	require.NoError(t, err)
	require.Equal(t, "verify_one", capacity.State)
	require.Equal(t, 1, capacity.InFlight)
	require.Equal(t, 1, capacity.Available, "releasing the unsent probe restores the one free slot")
	require.NoError(t, cache.USTCRelease(ctx, stream))
}

func TestUSTCCapacityExpiredLeaseFreesParallelButKeepsSentRPM(t *testing.T) {
	cache, client, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 5, Parallel: 2}
	const scope = "expired-lease"
	primeUSTCCapacity(t, cache, scope, limits)

	ticket, _, err := cache.USTCReserve(ctx, scope, limits)
	require.NoError(t, err)
	committed, err := cache.USTCCommit(ctx, ticket, limits)
	require.NoError(t, err)
	require.True(t, committed)
	serverTime, err := client.Time(ctx).Result()
	require.NoError(t, err)
	keys, err := cache.ustcCapacityKeys(scope)
	require.NoError(t, err)
	// Force only the lease score into the past while the fixed RPM window is
	// still live, isolating lease cleanup from window rollover.
	require.NoError(t, client.ZAdd(ctx, keys[1], redis.Z{Score: float64(serverTime.UnixMilli() - 1), Member: ticket.ID}).Err())

	capacity, err := cache.USTCRead(ctx, scope, limits)
	require.NoError(t, err)
	require.Zero(t, capacity.InFlight)
	require.Equal(t, 2, capacity.Used, "expired lease cleanup must preserve sent RPM in its live window")
	require.NoError(t, cache.USTCRelease(ctx, ticket))
}

func TestUSTCCapacityLostUnansweredMatureCommitWaitsForRecoveryProbe(t *testing.T) {
	cache, client, server := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	server.SetTime(time.Date(2026, time.January, 2, 12, 0, 0, 0, time.UTC))
	limits := service.USTCLimits{RPM: 5, Parallel: 2}
	const scope = "lost-unanswered-mature"
	primeUSTCCapacity(t, cache, scope, limits)
	advanceUSTCRedisTime(t, cache, server, 61*time.Second)

	ticket, _, err := cache.USTCReserve(ctx, scope, limits)
	require.NoError(t, err)
	require.NotNil(t, ticket)
	committed, err := cache.USTCCommit(ctx, ticket, limits)
	require.NoError(t, err)
	require.True(t, committed)
	serverTime, err := client.Time(ctx).Result()
	require.NoError(t, err)
	keys, err := cache.ustcCapacityKeys(scope)
	require.NoError(t, err)
	require.NoError(t, client.ZAdd(ctx, keys[1], redis.Z{Score: float64(serverTime.UnixMilli() - 1), Member: ticket.ID}).Err())

	capacity, err := cache.USTCRead(ctx, scope, limits)
	require.NoError(t, err)
	require.Equal(t, "sync_wait", capacity.State, "a lost response must start conservative synchronization")
	require.Equal(t, 1, capacity.Used, "lease loss does not refund the sent request")
	require.Zero(t, capacity.InFlight)
	require.Zero(t, capacity.Available)
	require.NoError(t, cache.USTCRelease(ctx, ticket))

	advanceUSTCRedisTime(t, cache, server, 61*time.Second)
	probe, _, err := cache.USTCReserve(ctx, scope, limits)
	require.NoError(t, err)
	require.NotNil(t, probe)
	require.True(t, probe.Probe, "the conservative wait expires into exactly one recovery probe")
	require.False(t, probe.Cold)
	committed, err = cache.USTCCommit(ctx, probe, limits)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.USTCObserve(ctx, probe, service.USTCFeedback{StatusCode: 200}))
	capacity, err = cache.USTCRead(ctx, scope, limits)
	require.NoError(t, err)
	require.Equal(t, "ready", capacity.State)
	require.Equal(t, 1, capacity.Used)
	require.NoError(t, cache.USTCRelease(ctx, probe))
}

func TestUSTCCapacityScopeRotationStartsIndependentState(t *testing.T) {
	cache, _, _ := newUSTCCapacityTestCache(t)
	ctx := context.Background()
	limits := service.USTCLimits{RPM: 1}
	primeUSTCCapacity(t, cache, "rotated-key-v1", limits)
	blocked, _, err := cache.USTCReserve(ctx, "rotated-key-v1", limits)
	require.NoError(t, err)
	require.Nil(t, blocked)

	newKeyProbe, capacity, err := cache.USTCReserve(ctx, "rotated-key-v2", limits)
	require.NoError(t, err)
	require.NotNil(t, newKeyProbe)
	require.True(t, newKeyProbe.Probe)
	require.Equal(t, "verify_one", capacity.State)
	require.Zero(t, capacity.Used)
	require.NoError(t, cache.USTCRelease(ctx, newKeyProbe))
}

func TestUSTCCapacityRedisFailureAndInvalidInputsFailClosed(t *testing.T) {
	cache, client, _ := newUSTCCapacityTestCache(t)
	require.NoError(t, client.Close())
	_, err := cache.USTCRead(context.Background(), "redis-error", service.USTCLimits{RPM: 1})
	require.Error(t, err)
	ticket, _, err := cache.USTCReserve(context.Background(), "redis-error", service.USTCLimits{RPM: 1})
	require.Error(t, err)
	require.Nil(t, ticket)

	cache, _, _ = newUSTCCapacityTestCache(t)
	_, err = cache.USTCRead(context.Background(), "", service.USTCLimits{RPM: 1})
	require.Error(t, err)
	_, _, err = cache.USTCReserve(context.Background(), "valid", service.USTCLimits{RPM: -1})
	require.Error(t, err)
}
