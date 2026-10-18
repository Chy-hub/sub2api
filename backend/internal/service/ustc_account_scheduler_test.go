package service

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Routing tests use a controlled store. Redis tests exercise the real atomic
// window/recovery implementation, avoiding a second copy of that state machine.
type ustcTestCapacityCache struct {
	RPMCache
	mu      sync.Mutex
	used    map[string]int
	tickets map[string]*ustcTestTicket
	serial  int
	blocked map[string]time.Time
}
type ustcTestTicket struct {
	scope string
	sent  bool
}

func (c *ustcTestCapacityCache) capacity(scope string, limits USTCLimits) USTCCapacity {
	inFlight, pending := 0, 0
	for _, ticket := range c.tickets {
		if ticket.scope == scope {
			inFlight++
			if !ticket.sent {
				pending++
			}
		}
	}
	state := "ready"
	if time.Now().Before(c.blocked[scope]) {
		state = "sync_wait"
	}
	reset := time.Now().Add(time.Minute)
	if state == "sync_wait" {
		reset = c.blocked[scope]
	}
	return USTCCapacity{State: state, RPMLimit: &limits.RPM, ParallelLimit: &limits.Parallel, Used: c.used[scope], Pending: pending, InFlight: inFlight, Available: max(0, min(limits.RPM-c.used[scope]-pending, limits.Parallel-inFlight)), ResetAt: reset}
}
func (c *ustcTestCapacityCache) USTCRead(_ context.Context, scope string, limits USTCLimits) (USTCCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity(scope, limits), nil
}
func (c *ustcTestCapacityCache) USTCReserve(_ context.Context, scope string, limits USTCLimits) (*USTCTicket, USTCCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.capacity(scope, limits)
	if value.Available <= 0 || value.State == "sync_wait" {
		return nil, value, nil
	}
	c.serial++
	id := fmt.Sprint(c.serial)
	c.tickets[id] = &ustcTestTicket{scope: scope}
	return &USTCTicket{Scope: scope, ID: id, Epoch: 1}, c.capacity(scope, limits), nil
}
func (c *ustcTestCapacityCache) USTCCommit(_ context.Context, ticket *USTCTicket, _ USTCLimits) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.tickets[ticket.ID]
	if entry == nil || entry.sent || time.Now().Before(c.blocked[ticket.Scope]) {
		return false, nil
	}
	entry.sent = true
	c.used[ticket.Scope]++
	return true, nil
}
func (c *ustcTestCapacityCache) USTCRelease(_ context.Context, ticket *USTCTicket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tickets, ticket.ID)
	return nil
}
func (c *ustcTestCapacityCache) USTCRenew(context.Context, *USTCTicket) error { return nil }
func (c *ustcTestCapacityCache) USTCObserve(context.Context, *USTCTicket, USTCFeedback) error {
	return nil
}
func (c *ustcTestCapacityCache) USTCCooldown(_ context.Context, scope string, delay time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[scope] = time.Now().Add(delay)
	return nil
}

type ustcTestStickyCache struct {
	schedulerTestGatewayCache
	mu sync.Mutex
}

func (c *ustcTestStickyCache) GetSessionAccountID(ctx context.Context, group int64, hash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, group, hash)
}
func (c *ustcTestStickyCache) SetSessionAccountID(ctx context.Context, group int64, hash string, id int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.SetSessionAccountID(ctx, group, hash, id, ttl)
}

func ustcSchedulerFixture(n int, batch bool) (*OpenAIGatewayService, *ustcTestCapacityCache) {
	accounts := make([]Account, n)
	for i := range accounts {
		lastUsed := time.Now().Add(time.Duration(i) * -time.Hour)
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 20, LastUsedAt: &lastUsed,
			Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": fmt.Sprintf("test-key-%d", i)},
			Extra:       map[string]any{"base_rpm": 20, "rpm_strategy": "sticky_exempt", "rpm_sticky_buffer": 5, "upstream_userinfo_limits_known": true, "upstream_userinfo_rpm_limit": 20, "upstream_userinfo_max_parallel_requests": 20},
		}
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cache := &ustcTestCapacityCache{used: make(map[string]int), tickets: make(map[string]*ustcTestTicket), blocked: make(map[string]time.Time)}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &ustcTestStickyCache{}, cfg: cfg, rpmCache: cache}
	return svc, cache
}

// setUSTCBudgetWindows overwrites the account's budget snapshot with `used`
// percent consumed in every window, observed just now.
func setUSTCBudgetWindows(account *Account, used ...float64) {
	windows := make([]UserInfoBudgetWindow, 0, len(used))
	for _, percent := range used {
		windows = append(windows, UserInfoBudgetWindow{Limit: 100, WindowSpend: percent, UsedKnown: true})
	}
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = windows
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated)] = time.Now().UTC().Format(time.RFC3339)
}

// setUSTCBudgetWindowAge backdates the snapshot, as if the probe had stopped.
func setUSTCBudgetWindowAge(account *Account, age time.Duration) {
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated)] = time.Now().Add(-age).UTC().Format(time.RFC3339)
}

func TestUSTCQuotaRemainingComparesCommonWindowsTightestFirst(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{
		{Duration: "3h", Limit: 30, WindowSpend: 1.2, UsedKnown: true},
		{Duration: "12h", Limit: 70, WindowSpend: 9.0, UsedKnown: true},
		{Duration: "24h", Limit: 100, WindowSpend: 50.0, UsedKnown: true},
	}
	setUSTCBudgetWindowAge(account, 0)
	remaining, trusted := ustcQuotaRemainingWindows(account, now)
	require.True(t, trusted)
	byKey := make(map[string]ustcQuotaRemaining, len(remaining))
	for _, window := range remaining {
		byKey[window.key] = window
	}
	require.InDelta(t, 28.8, byKey["3h"].remaining, 1e-9)
	require.InDelta(t, 61.0, byKey["12h"].remaining, 1e-9)
	require.InDelta(t, 50.0, byKey["24h"].remaining, 1e-9)
	// 3h has 96% left, 12h 87%, 24h 50%: the 24h window is the binding one.
	require.Equal(t, []string{"24h", "12h", "3h"}, ustcQuotaWindowOrder(remaining))

	order := []string{"3h", "12h", "24h"}
	// The tightest window decides first, and more remaining wins there.
	require.Negative(t, compareUSTCQuotaRemaining(
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 29.0}},
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}},
		order,
	))
	// A tie on the tightest window falls through to the next one.
	require.Negative(t, compareUSTCQuotaRemaining(
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}, {key: "12h", limit: 70, remaining: 61.0}},
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}, {key: "12h", limit: 70, remaining: 60.0}},
		order,
	))
	require.Zero(t, compareUSTCQuotaRemaining(
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}, {key: "12h", limit: 70, remaining: 61.0}},
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}, {key: "12h", limit: 70, remaining: 61.0}},
		order,
	))
	// Remaining quota is the metric: 80 left outranks 20 left regardless of how
	// much either key has already spent.
	require.Negative(t, compareUSTCQuotaRemaining(
		[]ustcQuotaRemaining{{key: "3h", limit: 100, remaining: 80.0}},
		[]ustcQuotaRemaining{{key: "3h", limit: 100, remaining: 20.0}},
		order,
	))
	// A window only one side reports must be skipped, not treated as empty.
	require.Zero(t, compareUSTCQuotaRemaining(
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}, {key: "12h", limit: 70, remaining: 61.0}},
		[]ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 28.0}},
		order,
	), "missing windows carry no ordering signal")
	// No budget windows at all also carries no ordering signal.
	require.Zero(t, compareUSTCQuotaRemaining(nil, []ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 29.0}}, order))

	// A nearly exhausted 12h window must outrank a barely touched 3h window;
	// ordering by duration would compare 3h first and never see the 12h deficit.
	nearlyDone := []ustcQuotaRemaining{
		{key: "3h", limit: 30, remaining: 29.9},
		{key: "12h", limit: 70, remaining: 1.0},
	}
	ample := []ustcQuotaRemaining{
		{key: "3h", limit: 30, remaining: 29.8},
		{key: "12h", limit: 70, remaining: 80.0},
	}
	order = ustcQuotaWindowOrder(nearlyDone, ample)
	require.Equal(t, "12h", order[0])
	require.Positive(t, compareUSTCQuotaRemaining(nearlyDone, ample, order),
		"the key about to exhaust its 12h budget must not win on a 0.1 3h difference")
}

func TestUSTCQuotaRemainingWindowsSkipUnusableEntries(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{
		{Duration: "3h", Limit: 100, WindowSpend: 100, UsedKnown: true, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)},
		{Duration: "12h", Limit: 100, UsedKnown: false},
		{Duration: "24h", Limit: 0, WindowSpend: 5, UsedKnown: true},
		{Duration: "24h", Limit: math.NaN(), WindowSpend: 5, UsedKnown: true},
		{Duration: "24h", Limit: 100, WindowSpend: 21, UsedKnown: true},
	}
	setUSTCBudgetWindowAge(account, 0)
	remaining, trusted := ustcQuotaRemainingWindows(account, now)
	require.True(t, trusted)
	require.Equal(t, []ustcQuotaRemaining{{key: "24h", limit: 100, remaining: 79}}, remaining,
		"reset, unknown usage and invalid limits are not constraints")

	// Snapshots read back from the database arrive as decoded JSON, not as the
	// typed struct: remaining must come out the same either way.
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []any{map[string]any{"duration": "3h", "limit": 100.0, "window_spend": 30.0, "used_known": true}}
	remaining, _ = ustcQuotaRemainingWindows(account, now)
	require.Equal(t, []ustcQuotaRemaining{{key: "3h", limit: 100, remaining: 70}}, remaining)

	// A probe that reports no budget persists max_budget: 0 and spend: 0, which
	// the compatibility path turns into a single Limit 0 window.
	delete(account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows))
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget)] = 0.0
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 0.0
	remaining, _ = ustcQuotaRemainingWindows(account, now)
	require.Nil(t, remaining, "a key without a budget has nothing to balance")
}

func TestUSTCQuotaRemainingIgnoresSharedUserWindow(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{
		{Duration: "3h", Scope: UserInfoWindowScopeKey, Limit: 30, WindowSpend: 1, UsedKnown: true},
		{Duration: "24h", Scope: UserInfoWindowScopeUser, Limit: 100, WindowSpend: 99, UsedKnown: true},
	}
	setUSTCBudgetWindowAge(account, 0)
	remaining, _ := ustcQuotaRemainingWindows(account, now)
	require.Equal(t, []ustcQuotaRemaining{{key: "3h", limit: 30, remaining: 29}}, remaining,
		"a shared user budget must not outrank per-key windows")
}

func TestUSTCQuotaRemainingTrustOnlyGuardsLongProbeFailures(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	setUSTCBudgetWindows(account, 1)
	_, trusted := ustcQuotaRemainingWindows(account, now)
	require.True(t, trusted)

	// Sparse traffic leaves snapshots older than the 30s refresh cadence; that
	// must not disable ordering. Only a probe that has been failing for a long
	// time loses trust, and even then the last known quota stays comparable.
	setUSTCBudgetWindowAge(account, 5*time.Minute)
	remaining, trusted := ustcQuotaRemainingWindows(account, now)
	require.True(t, trusted, "minutes-old snapshots still rank low-load traffic")
	require.NotEmpty(t, remaining)

	setUSTCBudgetWindowAge(account, ustcQuotaRemainingTrustedAge+time.Second)
	require.False(t, func() bool {
		_, ok := ustcQuotaRemainingWindows(account, now)
		return ok
	}())

	// A snapshot without any timestamp is equally untrustworthy.
	setUSTCBudgetWindowAge(account, 0)
	delete(account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated))
	_, trusted = ustcQuotaRemainingWindows(account, now)
	require.False(t, trusted)
}

type ustcTestQuotaRefresher struct{ used float64 }

func (f ustcTestQuotaRefresher) RefreshForScheduling(_ context.Context, account *Account) (*Account, error) {
	copy := copyAccountForUserInfoQuotaRefresh(account)
	copy.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{{Limit: 10, WindowSpend: f.used, UsedKnown: true}}
	return copy, nil
}

func TestUSTCLowConcurrencyPrefersTheLessUsedKey(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 45)
	setUSTCBudgetWindows(&accounts[1], 8)
	ctx := context.Background()
	// The first key is a whole usage band ahead, so it keeps losing even after
	// the second key has committed against an otherwise empty 60s window.
	for range 3 {
		selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
		require.NoError(t, err)
		require.Equal(t, int64(2), selection.Account.ID)
		require.True(t, selection.ustcAdmission.commit(ctx))
		selection.ustcAdmission.release()
		selection.ReleaseFunc()
	}
}

func TestUSTCLowLoadBudgetOrderingSurvivesConcurrentStreams(t *testing.T) {
	svc, cache := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 45)
	setUSTCBudgetWindows(&accounts[1], 8)
	// Three long streams occupy the least-used key. That is still low load for a
	// 20-parallel key, so budget balancing must keep working instead of falling
	// back to capacity order merely because in-flight requests outnumber keys.
	scope := USTCKeyScope(&accounts[1])
	cache.mu.Lock()
	for i := range 3 {
		cache.tickets[fmt.Sprintf("inflight-%d", i)] = &ustcTestTicket{scope: scope, sent: true}
	}
	cache.mu.Unlock()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	selection.ReleaseFunc()
}

func TestUSTCPoolHighConcurrencyCountsPhysicalKeySlotsOnce(t *testing.T) {
	svc, _ := ustcSchedulerFixture(6, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	for i := range 4 {
		accounts[i].Credentials["api_key"] = "shared-key"
	}
	candidates := make([]*Account, 0, len(accounts))
	capacities := make(map[int64]USTCCapacity, len(accounts))
	for i := range accounts {
		candidates = append(candidates, &accounts[i])
		// Every duplicate row of the shared key reports the same single free slot.
		capacities[accounts[i].ID] = USTCCapacity{Available: 1}
	}
	// Three physical keys with one free slot each can absorb three waiters.
	ctx := context.WithValue(context.Background(), ustcPoolDemandKey{}, 3)
	require.False(t, ustcPoolHighConcurrency(ctx, candidates, capacities),
		"counting duplicate rows would overstate capacity and hide pressure")
	ctx = context.WithValue(context.Background(), ustcPoolDemandKey{}, 4)
	require.True(t, ustcPoolHighConcurrency(ctx, candidates, capacities),
		"more waiters than remaining slots is capacity pressure")
}

func TestUSTCDispatchPassDoesNotDoubleCountItsOwnReservations(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 45)
	setUSTCBudgetWindows(&accounts[1], 8)
	state := svc.defaultUSTCPoolState()
	coordinator := &ustcPoolCoordinator{service: svc, state: state, scope: "group_0", wake: make(chan struct{}, 1)}
	ctx := context.WithValue(context.Background(), ustcBoundAccountKey{}, true)
	jobs := make([]*ustcPoolJob, 2)
	for i := range jobs {
		jobs[i] = &ustcPoolJob{ctx: ctx, accounts: []Account{accounts[0], accounts[1]}, model: "deepseek-flash", capability: OpenAIEndpointCapabilityResponses, result: make(chan ustcPoolResult, 1)}
	}
	coordinator.jobs = append([]*ustcPoolJob(nil), jobs...)
	state.coordinators[coordinator.scope] = coordinator
	go coordinator.run()
	// Two requests against two accounts stay below the high-concurrency threshold
	// for the whole pass: the lease the first job took is visible to the second
	// through the shared reads, so it must not be counted as a waiter as well.
	for _, job := range jobs {
		select {
		case result := <-job.result:
			require.NoError(t, result.err)
			require.Equal(t, int64(2), result.selection.Account.ID)
			// Held, not released: the lease must stay visible to the next job.
		case <-time.After(time.Second):
			t.Fatal("waiter did not finish within its bounded wait")
		}
	}
}

func TestUSTCDemandIgnoresWaitersThatAreAlreadyGone(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 45)
	setUSTCBudgetWindows(&accounts[1], 8)
	state := svc.defaultUSTCPoolState()
	coordinator := &ustcPoolCoordinator{service: svc, state: state, scope: "group_0", wake: make(chan struct{}, 1)}
	ctx := context.WithValue(context.Background(), ustcBoundAccountKey{}, true)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	timedOut := time.Now().Add(-time.Second)
	// One live waiter at the head, a cancelled and a timed-out waiter behind it.
	// Counting the dead ones would read as three concurrent requests against two
	// accounts and flip the live one into the high-concurrency order.
	jobs := []*ustcPoolJob{
		{ctx: ctx, accounts: []Account{accounts[0], accounts[1]}, model: "deepseek-flash", capability: OpenAIEndpointCapabilityResponses, result: make(chan ustcPoolResult, 1)},
		{ctx: cancelled, accounts: []Account{accounts[0], accounts[1]}, model: "deepseek-flash", capability: OpenAIEndpointCapabilityResponses, result: make(chan ustcPoolResult, 1)},
		{ctx: ctx, deadline: timedOut, accounts: []Account{accounts[0], accounts[1]}, model: "deepseek-flash", capability: OpenAIEndpointCapabilityResponses, result: make(chan ustcPoolResult, 1)},
	}
	coordinator.jobs = append([]*ustcPoolJob(nil), jobs...)
	state.coordinators[coordinator.scope] = coordinator
	go coordinator.run()
	select {
	case result := <-jobs[0].result:
		require.NoError(t, result.err)
		require.Equal(t, int64(2), result.selection.Account.ID)
	case <-time.After(time.Second):
		t.Fatal("live waiter did not finish within its bounded wait")
	}
	select {
	case result := <-jobs[1].result:
		require.ErrorIs(t, result.err, context.Canceled)
		require.Nil(t, result.selection)
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter was not retired")
	}
	select {
	case result := <-jobs[2].result:
		require.Nil(t, result.selection)
	case <-time.After(time.Second):
		t.Fatal("timed-out waiter was not retired")
	}
}

func TestUSTCModeratelyOldQuotaStillOrdersLowLoad(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 0)
	// Sparse low-load traffic regularly leaves snapshots minutes old. Treating
	// that as "unknown" would silently disable budget balancing exactly when it
	// is needed, so only a long-running probe failure loses trust.
	setUSTCBudgetWindowAge(&accounts[0], 5*time.Minute)
	setUSTCBudgetWindows(&accounts[1], 8)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID, "minutes-old spend must still rank the least-used key first")
	selection.ReleaseFunc()
}

func TestUSTCLongDeadQuotaDoesNotOverruleFreshData(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 0)
	setUSTCBudgetWindowAge(&accounts[0], ustcQuotaRemainingTrustedAge+time.Minute)
	setUSTCBudgetWindows(&accounts[1], 8)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID, "a long-dead probe must not rank its key ahead of a fresh one")
	selection.ReleaseFunc()
}

func TestUSTCThreeKeysDistributeConcurrentTrafficWithoutStickyOverflow(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			svc, cache := ustcSchedulerFixture(3, batch)
			var workers sync.WaitGroup
			errors := make(chan error, 60)
			for range 60 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "same-session", "deepseek-flash", nil)
					if err != nil {
						errors <- err
						return
					}
					if !selection.ustcAdmission.commit(context.Background()) {
						errors <- fmt.Errorf("commit rejected")
					}
					selection.ustcAdmission.release() // Simulate closing the upstream body.
					selection.ReleaseFunc()
				}()
			}
			workers.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			cache.mu.Lock()
			for _, count := range cache.used {
				require.Equal(t, 20, count)
			}
			require.Len(t, cache.used, 3)
			require.Empty(t, cache.tickets)
			cache.mu.Unlock()
			_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "same-session", "deepseek-flash", nil)
			retry, capacity := USTCPoolRetryAfter(err)
			require.True(t, capacity)
			require.GreaterOrEqual(t, retry, 59)
		})
	}
}
func TestUSTCAbandonedSelectionRefundsAndSentFailuresKeepRPM(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	ctx := context.Background()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	selection.ReleaseFunc()
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	value, err := cache.USTCRead(ctx, USTCKeyScope(&account), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Zero(t, value.Used)
	require.Zero(t, value.Pending)
	require.Zero(t, value.InFlight)
	selection, err = svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.True(t, selection.ustcAdmission.commit(ctx))
	selection.ustcAdmission.release()
	selection.ReleaseFunc()
	value, err = cache.USTCRead(ctx, USTCKeyScope(&account), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Equal(t, 1, value.Used)
	require.Zero(t, value.InFlight)
}
func TestUSTCDuplicateAccountRowsSharePhysicalKeyCapacity(t *testing.T) {
	svc, cache := ustcSchedulerFixture(3, false)
	repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	for i := range repo.accounts {
		repo.accounts[i].Credentials["api_key"] = "same-physical-key"
	}
	for range 20 {
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
		require.NoError(t, err)
		require.True(t, selection.ustcAdmission.commit(context.Background()))
		selection.ustcAdmission.release()
		selection.ReleaseFunc()
	}
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	cache.mu.Lock()
	require.Len(t, cache.used, 1)
	cache.mu.Unlock()
	oldScope := USTCKeyScope(&repo.accounts[0])
	repo.accounts[0].Credentials["api_key"] = "rotated-key"
	require.NotEqual(t, oldScope, USTCKeyScope(&repo.accounts[0]))
}
func TestUSTCUnknownLimitsNeverUseManualRPMOrUnlimitedFallback(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := &svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	delete(account.Extra, "upstream_userinfo_limits_known")
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.Nil(t, selection)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	account.Extra["upstream_userinfo_limits_known"] = true
	svc.rpmCache = nil
	selection, err = svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.Nil(t, selection)
	_, capacity = USTCPoolRetryAfter(err)
	require.True(t, capacity)
}

type ustcTransportStub struct {
	HTTPUpstream
	calls int
}

func (s *ustcTransportStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")), Request: req}, nil
}
func TestUSTCHTTPTransportCommitsAtSendAndReleasesBodyForEveryAttempt(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcTransportStub{}
	svc.httpUpstream = upstream
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	value, _ := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Zero(t, value.Used)
	require.Equal(t, 1, value.Pending)
	req, _ := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), "POST", "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	resp, err := svc.doOpenAIUpstream(req, "", &account)
	require.NoError(t, err)
	value, _ = cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Equal(t, 1, value.Used)
	require.Equal(t, 1, value.InFlight)
	require.NoError(t, resp.Body.Close())
	resp, err = svc.doOpenAIUpstream(req, "", &account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	selection.ReleaseFunc()
	value, _ = cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Equal(t, 2, value.Used)
	require.Zero(t, value.InFlight)
	require.Equal(t, 2, upstream.calls)
}
