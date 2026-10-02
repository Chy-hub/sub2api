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

func TestUSTCPoolBoundWaiterDoesNotBlockAnotherReadyKey(t *testing.T) {
	svc, cache := ustcSchedulerFixture(2, false)
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 2 * time.Minute
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 4
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	cache.used[USTCKeyScope(&accounts[0])] = 20
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), ustcBoundAccountKey{}, true))
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := svc.selectBalancedDefaultUSTCAccountWithWait(ctx, nil, accounts[:1], "", "deepseek-flash", nil, false, "", false, OpenAIUpstreamTransportAny)
		finished <- err
	}()
	state := svc.defaultUSTCPoolState()
	require.Eventually(t, func() bool { state.mu.Lock(); defer state.mu.Unlock(); return state.waiters["group_0"] == 1 }, time.Second, time.Millisecond)
	started := time.Now()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.Less(t, time.Since(started), 200*time.Millisecond)
	selection.ReleaseFunc()
	cancel()
	require.ErrorIs(t, <-finished, context.Canceled)
}

func TestUSTCPoolFullCommittedRPMDoesNotWaitForLongStreamClose(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 2 * time.Second
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	scope := USTCKeyScope(&account)
	stream, _, err := cache.USTCReserve(context.Background(), scope, USTCLimits{20, 20})
	require.NoError(t, err)
	committed, err := cache.USTCCommit(context.Background(), stream, USTCLimits{20, 20})
	require.NoError(t, err)
	require.True(t, committed)
	defer cache.USTCRelease(context.Background(), stream)
	cache.mu.Lock()
	cache.used[scope] = 20 // Other committed requests have already finished.
	cache.mu.Unlock()
	started := time.Now()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.Nil(t, selection)
	retry, limited := USTCPoolRetryAfter(err)
	require.True(t, limited)
	require.GreaterOrEqual(t, retry, 59)
	require.Less(t, time.Since(started), 250*time.Millisecond, "stream close cannot restore spent RPM within the two-second wait budget")
}

func TestUSTCPoolBoundedQueueCancellationAndRefundWakeImmediately(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 2 * time.Second
	svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	var reservations []*ustcAdmission
	for range 20 {
		reservation, _, err := svc.reserveUSTC(context.Background(), &account)
		require.NoError(t, err)
		require.NotNil(t, reservation)
		reservations = append(reservations, reservation)
	}
	defer func() {
		for _, reservation := range reservations {
			reservation.release()
		}
	}()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	first := make(chan error, 1)
	go func() {
		selection, err := svc.SelectAccountWithLoadAwareness(firstCtx, nil, "", "deepseek-flash", nil)
		if selection != nil {
			selection.ReleaseFunc()
		}
		first <- err
	}()
	state := svc.defaultUSTCPoolState()
	count := func() int { state.mu.Lock(); defer state.mu.Unlock(); return state.waiters["group_0"] }
	require.Eventually(t, func() bool { return count() == 1 }, time.Second, time.Millisecond)
	second := make(chan ustcPoolResult, 1)
	go func() {
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
		second <- ustcPoolResult{selection, err}
	}()
	require.Eventually(t, func() bool { return count() == 2 }, time.Second, time.Millisecond)
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	firstCancel()
	require.ErrorIs(t, <-first, context.Canceled)
	require.Eventually(t, func() bool { return count() == 1 }, time.Second, time.Millisecond)
	started := time.Now()
	reservations[0].release()
	result := <-second
	require.NoError(t, result.err)
	require.NotNil(t, result.selection)
	require.Less(t, time.Since(started), 200*time.Millisecond, "a refund event must not wait for the one-second fallback")
	result.selection.ReleaseFunc()
	require.Eventually(t, func() bool { return count() == 0 }, time.Second, time.Millisecond)
	value, _ := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Equal(t, 19, value.Pending)
	require.Zero(t, value.Used)
}

func (r *ustcStablePoolRepo) ListUSTCAccountPool(_ context.Context, groupID *int64, _ bool) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.poolCalls++
	r.groupID = derefGroupID(groupID)
	return cloneUSTCPool(r.pool), nil
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
