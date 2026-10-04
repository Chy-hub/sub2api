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

func TestUSTCQuotaUsageBandsCompareMostConstrainedWindowFirst(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	setUSTCBudgetWindows(account, 45, 10)
	require.Equal(t, []int{9, 2}, ustcQuotaUsageBands(account, now))
	// The tightest window decides; later windows only break a tie in the first.
	require.Positive(t, compareUSTCQuotaUsageBands([]int{9, 2}, []int{4, 2}))
	require.Negative(t, compareUSTCQuotaUsageBands([]int{4, 2}, []int{4, 3}))
	require.Zero(t, compareUSTCQuotaUsageBands([]int{4, 2}, []int{4, 2}))

	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{
		{Limit: 100, WindowSpend: 100, UsedKnown: true, ResetAt: now.Add(-time.Minute).Format(time.RFC3339)},
		{Limit: 100, UsedKnown: false},
		{Limit: 100, WindowSpend: 21, UsedKnown: true},
	}
	require.Equal(t, []int{4}, ustcQuotaUsageBands(account, now), "reset and unknown windows are not constraints")

	// Snapshots read back from the database arrive as decoded JSON, not as the
	// typed struct: the bands must come out the same either way.
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []any{map[string]any{"limit": 100.0, "window_spend": 30.0, "used_known": true}}
	require.Equal(t, []int{6}, ustcQuotaUsageBands(account, now))

	// A key the upstream never reported a budget for has nothing to balance.
	delete(account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows))
	require.Nil(t, ustcQuotaUsageBands(account, now))
	require.Zero(t, compareUSTCQuotaUsageBands(nil, []int{0}))
	require.Less(t, compareUSTCQuotaUsageBands(nil, []int{1}), 0)
}

func TestUSTCUsageBandRoundsExactBoundaries(t *testing.T) {
	// 0.15/0.05 is 2.999… in float64; truncating would rank an account sitting
	// exactly on a band boundary one band better than it is.
	for _, tc := range []struct {
		used float64
		want int
	}{{0, 0}, {0.0499, 0}, {0.05, 1}, {0.1499, 2}, {0.15, 3}, {0.30, 6}, {0.60, 12}, {0.95, 19}, {1, 20}, {1.5, 20}} {
		require.Equal(t, tc.want, ustcUsageBand(tc.used), "used=%v", tc.used)
	}
	// Malformed ratios must rank last, never first.
	require.Equal(t, ustcQuotaUsageMaxBand, ustcUsageBand(math.NaN()))
	require.Equal(t, ustcQuotaUsageMaxBand, ustcUsageBand(math.Inf(1)))
	require.Equal(t, 0, ustcUsageBand(math.Inf(-1)))
}

func TestUSTCNoBudgetSnapshotIsNeverDemoted(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	// A probe that reports no budget persists max_budget: 0 and spend: 0, which
	// the compatibility path turns into a single Limit 0 window.
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget)] = 0.0
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 0.0
	require.Nil(t, ustcQuotaUsageBands(account, now))
	setUSTCBudgetWindowAge(account, 3*time.Hour)
	require.Nil(t, ustcQuotaUsageBands(account, now), "a key without a budget has nothing to balance, stale or not")

	// A window that cannot produce a ratio is skipped, not ranked as a constraint.
	setUSTCBudgetWindows(account, 0)
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{
		{Limit: math.NaN(), WindowSpend: 5, UsedKnown: true},
		{Limit: 100, WindowSpend: 5, UsedKnown: true},
	}
	require.Equal(t, []int{1}, ustcQuotaUsageBands(account, now))
}

func TestUSTCStaleQuotaSnapshotIsNotUsedForOrdering(t *testing.T) {
	now := time.Now()
	account := &Account{Extra: map[string]any{}}
	setUSTCBudgetWindows(account, 0)
	require.Equal(t, []int{0}, ustcQuotaUsageBands(account, now))

	// Observations older than the refresh cadence must not rank the key: a
	// stale "0%" cannot outrank a freshly probed key, but the account stays
	// selectable until its background refresh lands.
	setUSTCBudgetWindowAge(account, ustcQuotaUsageTrustedAge+time.Second)
	require.Equal(t, []int{ustcQuotaUsageStaleBand}, ustcQuotaUsageBands(account, now))
	require.Positive(t, compareUSTCQuotaUsageBands(ustcQuotaUsageBands(account, now), []int{0}))

	// A snapshot without any timestamp is equally untrustworthy.
	delete(account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated))
	require.Equal(t, []int{ustcQuotaUsageStaleBand}, ustcQuotaUsageBands(account, now))
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

func TestUSTCHighConcurrencySpreadsLiveRequestsInsteadOfQuotaUsage(t *testing.T) {
	svc, cache := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 45)
	setUSTCBudgetWindows(&accounts[1], 8)
	// Three requests are already in the air on the least-used key: spreading
	// live concurrency now outranks its budget advantage, so the busier-by-budget
	// key takes this one.
	scope := USTCKeyScope(&accounts[1])
	cache.mu.Lock()
	for i := range 3 {
		cache.tickets[fmt.Sprintf("inflight-%d", i)] = &ustcTestTicket{scope: scope, sent: true}
	}
	cache.mu.Unlock()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)
	selection.ReleaseFunc()
}

func TestUSTCHighConcurrencyCountsPhysicalKeysNotDuplicateRows(t *testing.T) {
	svc, cache := ustcSchedulerFixture(6, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	for i := range 4 {
		accounts[i].Credentials["api_key"] = "shared-key"
		setUSTCBudgetWindows(&accounts[i], 8)
	}
	setUSTCBudgetWindows(&accounts[4], 45)
	setUSTCBudgetWindows(&accounts[5], 45)
	// Three physical keys, two requests in flight on the shared one: not high
	// concurrency. Counting its four rows separately would inflate both sides and
	// hand this request to a 45% key instead of the least-used one.
	scope := USTCKeyScope(&accounts[0])
	cache.mu.Lock()
	for i := range 2 {
		cache.tickets[fmt.Sprintf("shared-inflight-%d", i)] = &ustcTestTicket{scope: scope, sent: true}
	}
	cache.mu.Unlock()
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, scope, USTCKeyScope(selection.Account))
	selection.ReleaseFunc()
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

func TestUSTCStaleQuotaDoesNotDecideTheSelection(t *testing.T) {
	svc, _ := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	setUSTCBudgetWindows(&accounts[0], 0)
	setUSTCBudgetWindowAge(&accounts[0], ustcQuotaUsageTrustedAge+time.Minute)
	setUSTCBudgetWindows(&accounts[1], 8)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID, "a stale snapshot must not rank its key ahead of a fresh one")
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
