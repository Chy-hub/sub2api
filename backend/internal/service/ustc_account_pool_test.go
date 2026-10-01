package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcStablePoolRepo struct {
	schedulerTestOpenAIAccountRepo
	mu        sync.Mutex
	pool      []Account
	poolCalls int
	groupID   int64
}

func (r *ustcStablePoolRepo) ListUSTCAccountPool(_ context.Context, groupID *int64, _ bool) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.poolCalls++
	r.groupID = derefGroupID(groupID)
	return cloneUSTCPool(r.pool), nil
}

func TestUSTCPoolRestoresCoolingMembersMissingFromSchedulerSnapshot(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, true)
	ustc := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	groupID := int64(9)
	ustc.GroupIDs = []int64{groupID}
	coolingUntil := time.Now().Add(100 * time.Millisecond)
	ustc.RateLimitResetAt = &coolingUntil
	other := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, GroupIDs: []int64{groupID}}
	repo := &ustcStablePoolRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{ustc, other}}, pool: []Account{ustc}}
	svc.accountRepo = repo
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&other}, accountsByID: map[int64]*Account{1: &ustc, 2: &other},
	}}
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), &groupID, "", "deepseek-flash", nil)
	require.NoError(t, err, "a pool=1 model_not_supported snapshot must not hide the recovered USTC member")
	require.Equal(t, ustc.ID, selection.Account.ID)
	require.True(t, selection.Acquired)
	require.Nil(t, selection.WaitPlan)
	require.True(t, selection.RPMReserved())
	selection.ReleaseFunc()
	require.Equal(t, groupID, repo.groupID)
	require.Equal(t, 1, repo.poolCalls, "cooldown expiry must work without a DB membership or general bucket rebuild")
	count, err := rpm.GetRPM(context.Background(), ustc.ID)
	require.NoError(t, err)
	require.Zero(t, count, "releasing an unadmitted selection still refunds RPM")
}

type ustcRecoveringConcurrencyCache struct {
	schedulerTestConcurrencyCache
	mu           sync.Mutex
	available    map[int64]bool
	acquisitions []int64
}

func TestUSTCPoolQuotaProbeDoesNotIdleAvailableAccounts(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(3, true)
	upstream := newBlockingUserInfoQuotaUpstream()
	quota := NewUpstreamUserInfoQuotaService(userInfoQuotaConcurrentRepoStub{}, nil, upstream, nil)
	svc.ustcQuotaRefresher = quota
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 50 * time.Millisecond
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	defer func() {
		close(upstream.releaseKeyInfo)
		require.Eventually(t, func() bool {
			quota.refreshMu.Lock()
			defer quota.refreshMu.Unlock()
			return len(quota.refreshPending) == 0
		}, time.Second, time.Millisecond)
	}()
	ctx := context.Background()
	for range 18 {
		selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "same_session", "deepseek-flash", nil)
		require.NoError(t, err, "usable accounts must serve while the quota endpoint is blocked")
		require.True(t, selection.Acquired)
		require.Nil(t, selection.WaitPlan)
		admitted := svc.PrepareDefaultAccountRPM(ctx, selection)
		if admitted {
			selection.CommitRPMReservation()
		}
		selection.ReleaseFunc()
		require.True(t, admitted, "the final check must also avoid holding capacity for the slow probe")
	}
	counts, err := rpm.GetRPMBatch(ctx, []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 6, 2: 6, 3: 6}, counts, "slow quota probes must not disrupt balanced RPM accounting")
	state := svc.defaultUSTCPoolState()
	state.mu.Lock()
	require.Empty(t, state.waiters, "these requests must use idle capacity without joining the waiting queue")
	state.mu.Unlock()
}

func (c *ustcRecoveringConcurrencyCache) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.available[id] {
		return false, nil
	}
	c.acquisitions = append(c.acquisitions, id)
	return true, nil
}

func TestUSTCPoolWaitUsesAnyFreedAccountInsteadOfOriginalBusyAccount(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(2, true)
	cache := &ustcRecoveringConcurrencyCache{available: map[int64]bool{}}
	svc.concurrencyService = NewConcurrencyService(cache)
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	require.NoError(t, svc.cache.SetSessionAccountID(context.Background(), 0, "openai:session", 1, time.Hour))
	go func() {
		time.Sleep(50 * time.Millisecond)
		cache.mu.Lock()
		cache.available[2] = true
		cache.mu.Unlock()
	}()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "session", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID, "free account 2 must serve without waiting for sticky account 1")
	require.True(t, selection.Acquired)
	require.Nil(t, selection.WaitPlan)
	selection.ReleaseFunc()
	counts, err := rpm.GetRPMBatch(context.Background(), []int64{1, 2})
	require.NoError(t, err)
	require.Equal(t, map[int64]int{1: 0, 2: 0}, counts, "waiting must not consume RPM")
	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Equal(t, []int64{2}, cache.acquisitions, "waiting must not acquire or hold a busy account slot")
}

func TestUSTCPoolWaitImmediatelyUsesRefundedRPMCapacity(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	rpm.counts[1] = 20
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	go func() {
		time.Sleep(50 * time.Millisecond)
		rpm.mu.Lock()
		rpm.counts[1]--
		rpm.mu.Unlock()
	}()
	started := time.Now()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err, "a refunded slot must become usable before the next minute")
	require.Less(t, time.Since(started), time.Second)
	selection.CommitRPMReservation()
	selection.ReleaseFunc()
	count, err := rpm.GetRPM(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 20, count)
}

func TestUSTCPoolWaitBoundsOverloadAndReleasesCancelledWaiter(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	rpm.counts[1] = 20
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = time.Second
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		_, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
		firstResult <- err
	}()
	state := svc.defaultUSTCPoolState()
	require.Eventually(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.waiters["group_0"] == 1
	}, time.Second, time.Millisecond)
	started := time.Now()
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	retryAfter, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	require.GreaterOrEqual(t, retryAfter, 1)
	require.Contains(t, err.Error(), "ustc_pool_wait_full")
	require.Less(t, time.Since(started), 500*time.Millisecond, "queue overflow must not pile up unbounded requests")
	cancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	state.mu.Lock()
	require.Empty(t, state.waiters)
	state.mu.Unlock()
	count, err := rpm.GetRPM(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 20, count)
}

func TestUSTCPoolWaitTimeoutIsCapacityErrorAndUnsupportedModelDoesNotWait(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	rpm.counts[1] = 20
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Millisecond
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 1
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	state := svc.defaultUSTCPoolState()
	state.mu.Lock()
	require.Empty(t, state.waiters)
	state.mu.Unlock()
	repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	repo.accounts[0].Credentials["model_mapping"] = map[string]any{"another-model": "another-model"}
	started := time.Now()
	_, err = svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	_, capacity = USTCPoolRetryAfter(err)
	require.False(t, capacity, "model configuration errors must not consume a waiting permit")
	require.Less(t, time.Since(started), 20*time.Millisecond)
}

func TestUSTCPoolMembershipCoalescesAndReturnsIsolatedCopies(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	repo := &ustcStablePoolRepo{schedulerTestOpenAIAccountRepo: base, pool: base.accounts}
	svc.accountRepo = repo
	var workers sync.WaitGroup
	for range 30 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			accounts := svc.supplementDefaultUSTCPool(context.Background(), nil, nil)
			if len(accounts) == 1 {
				accounts[0].Extra["base_rpm"] = 999
			}
		}()
	}
	workers.Wait()
	pool := svc.supplementDefaultUSTCPool(context.Background(), nil, nil)
	require.Equal(t, 20, pool[0].GetBaseRPM())
	require.Equal(t, 1, repo.poolCalls)
}

type ustcCountingPollRPMCache struct {
	*ustcTestRPMCache
	batchMu sync.Mutex
	batches int
}

func (c *ustcCountingPollRPMCache) GetRPMBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	c.batchMu.Lock()
	c.batches++
	c.batchMu.Unlock()
	return c.ustcTestRPMCache.GetRPMBatch(ctx, ids)
}

func TestUSTCPoolWaitingPollsShareReadBatches(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(3, false)
	cache := &ustcCountingPollRPMCache{ustcTestRPMCache: rpm}
	svc.rpmCache = cache
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	candidates := []*Account{&accounts[0], &accounts[1], &accounts[2]}
	ctx := context.WithValue(context.Background(), ustcPoolPollKey{}, "group_9")
	var workers sync.WaitGroup
	for range 50 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			svc.defaultUSTCPoolPoll(ctx, []int64{1, 2, 3}, candidates)
		}()
	}
	workers.Wait()
	cache.batchMu.Lock()
	require.Equal(t, 1, cache.batches, "queued requests should share one read batch within the polling interval")
	cache.batchMu.Unlock()
	// Normal incoming selections still use live counters; a waiting read cache
	// must not make low-rate traffic flock to an already-used account.
	svc.defaultUSTCPoolPoll(context.Background(), []int64{1, 2, 3}, candidates)
	cache.batchMu.Lock()
	require.Equal(t, 2, cache.batches)
	cache.batchMu.Unlock()
}

type ustcBlockedPollRPMCache struct {
	*ustcTestRPMCache
	started chan struct{}
	resume  chan struct{}
}

func (c *ustcBlockedPollRPMCache) GetRPMBatch(ctx context.Context, ids []int64) (map[int64]int, error) {
	close(c.started)
	<-c.resume
	return c.ustcTestRPMCache.GetRPMBatch(ctx, ids)
}

func TestUSTCPoolCanceledPollOwnsCandidateInputs(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(2, true)
	cache := &ustcBlockedPollRPMCache{ustcTestRPMCache: rpm, started: make(chan struct{}), resume: make(chan struct{})}
	svc.rpmCache = cache
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	candidates := []*Account{&accounts[0], &accounts[1]}
	ids := []int64{1, 2}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ustcPoolPollKey{}, "group_9"))
	defer cancel()
	done := make(chan struct{})
	go func() {
		svc.defaultUSTCPoolPoll(ctx, ids, candidates)
		close(done)
	}()
	<-cache.started
	cancel()
	<-done
	// The canceled selection can now mutate its slices while the shared read
	// continues. Both RPM IDs and the later load request must retain both accounts.
	ids[0] = 2
	candidates[0] = candidates[1]
	close(cache.resume)
	state := svc.defaultUSTCPoolState()
	require.Eventually(t, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		entry, exists := state.polls["group_9"]
		return exists && len(entry.counts) == 2 && len(entry.loads) == 2
	}, time.Second, time.Millisecond)
}
