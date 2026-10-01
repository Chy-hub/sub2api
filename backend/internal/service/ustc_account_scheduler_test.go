package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ustcTestRPMCache struct {
	mu     sync.Mutex
	counts map[int64]int
}

func (c *ustcTestRPMCache) GetRPM(_ context.Context, id int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[id], nil
}
func (c *ustcTestRPMCache) GetRPMBatch(_ context.Context, ids []int64) (map[int64]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := make(map[int64]int, len(ids))
	for _, id := range ids {
		counts[id] = c.counts[id]
	}
	return counts, nil
}
func (c *ustcTestRPMCache) IncrementRPM(_ context.Context, id int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[id]++
	return c.counts[id], nil
}
func (c *ustcTestRPMCache) ReserveRPM(_ context.Context, id int64, limit int) (bool, func(), error) {
	c.mu.Lock()
	if limit > 0 && c.counts[id] >= limit {
		c.mu.Unlock()
		return false, nil, nil
	}
	c.counts[id]++
	c.mu.Unlock()
	var once sync.Once
	return true, func() { once.Do(func() { c.mu.Lock(); c.counts[id]--; c.mu.Unlock() }) }, nil
}

type ustcTestStickyCache struct {
	schedulerTestGatewayCache
	mu sync.Mutex
}

func (c *ustcTestStickyCache) GetSessionAccountID(ctx context.Context, groupID int64, hash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, groupID, hash)
}
func (c *ustcTestStickyCache) SetSessionAccountID(ctx context.Context, groupID int64, hash string, id int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.SetSessionAccountID(ctx, groupID, hash, id, ttl)
}

func ustcSchedulerFixture(n int, batch bool) (*OpenAIGatewayService, *ustcTestRPMCache) {
	accounts := make([]Account, n)
	for i := range accounts {
		lastUsed := time.Now().Add(time.Duration(i) * -time.Hour)
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: 20, LastUsedAt: &lastUsed,
			Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "test"},
			Extra:       map[string]any{"base_rpm": 20, "rpm_strategy": "tiered", "rpm_sticky_buffer": 5},
		}
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	rpm := &ustcTestRPMCache{counts: make(map[int64]int)}
	return &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache: &ustcTestStickyCache{}, cfg: cfg, rpmCache: rpm,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
	}, rpm
}

func TestUSTCDefaultSchedulerBalancesSameSessionAndHonorsBuffer(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("load_batch_%t", batch), func(t *testing.T) {
			svc, rpm := ustcSchedulerFixture(3, batch)
			ctx := context.Background()
			for i := 0; i < 60; i++ {
				selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "claude-code-session", "deepseek-flash", nil)
				require.NoError(t, err)
				require.True(t, selection.RPMReserved())
				require.True(t, svc.PrepareDefaultAccountRPM(ctx, selection))
				selection.CommitRPMReservation()
				selection.ReleaseFunc()
			}
			counts, _ := rpm.GetRPMBatch(ctx, []int64{1, 2, 3})
			require.Equal(t, map[int64]int{1: 20, 2: 20, 3: 20}, counts)
			var sticky int64
			for i := 0; i < 5; i++ {
				selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "claude-code-session", "deepseek-flash", nil)
				require.NoError(t, err)
				sticky = selection.Account.ID
				selection.CommitRPMReservation()
				selection.ReleaseFunc()
			}
			count, _ := rpm.GetRPM(ctx, sticky)
			require.Equal(t, 25, count)
			_, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "claude-code-session", "deepseek-flash", nil)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
}

func TestUSTCDefaultSchedulerPublicGatewayEntryWithExperimentDisabled(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc, rpm := ustcSchedulerFixture(3, false)
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("false")
	for i := 0; i < 6; i++ {
		selection, _, err := svc.SelectAccountWithScheduler(context.Background(), nil, "", "claude-code-session", "deepseek-flash", nil, OpenAIUpstreamTransportAny, false)
		require.NoError(t, err)
		require.True(t, selection.RPMReserved())
		selection.CommitRPMReservation()
		selection.ReleaseFunc()
	}
	counts, _ := rpm.GetRPMBatch(context.Background(), []int64{1, 2, 3})
	require.Equal(t, map[int64]int{1: 2, 2: 2, 3: 2}, counts)
}

func TestUSTCDefaultSchedulerDoesNotReserveHTTPRPMForNativeWebSocketAdmission(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	ctx := context.WithValue(context.Background(), skipDefaultUSTCBalancingKey{}, true)
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.False(t, selection.RPMReserved())
	selection.ReleaseFunc()
	count, _ := rpm.GetRPM(ctx, 1)
	require.Zero(t, count)
}

func TestUSTCDefaultSchedulerParallelAdmissionDoesNotOvershootRPM(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(8, true)
	ctx := context.Background()
	var wg sync.WaitGroup
	errors := make(chan error, 160)
	start := make(chan struct{})
	for i := 0; i < 160; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "claude-code-session", "deepseek-flash", nil)
			if err != nil {
				errors <- err
				return
			}
			selection.CommitRPMReservation()
			selection.ReleaseFunc()
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	counts, _ := rpm.GetRPMBatch(ctx, []int64{1, 2, 3, 4, 5, 6, 7, 8})
	total := 0
	for _, count := range counts {
		require.Positive(t, count)
		require.LessOrEqual(t, count, 25)
		total += count
	}
	require.Equal(t, 160, total)
}

func TestUSTCDefaultSchedulerRefundsOnlyUnadmittedRequests(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	ctx := context.Background()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	selection.ReleaseFunc()
	selection.ReleaseRPMReservation()
	count, _ := rpm.GetRPM(ctx, 1)
	require.Zero(t, count)
	selection, err = svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	selection.CommitRPMReservation()
	selection.ReleaseFunc() // An upstream error still consumes an attempted request.
	count, _ = rpm.GetRPM(ctx, 1)
	require.Equal(t, 1, count)
}

func TestUSTCDefaultSchedulerRechecksRPMAndQuotaAfterWaiting(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, true)
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false}})
	ctx := context.Background()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.NotNil(t, selection.WaitPlan)
	require.False(t, selection.RPMReserved())
	rpm.counts[1] = 20
	require.False(t, svc.PrepareDefaultAccountRPM(ctx, selection))
	rpm.counts[1] = 0
	selection.Account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixValid)] = false
	require.False(t, svc.PrepareDefaultAccountRPM(ctx, selection))
	count, _ := rpm.GetRPM(ctx, 1)
	require.Zero(t, count)
}

type ustcTestQuotaRefresher struct{ used float64 }

func (f ustcTestQuotaRefresher) RefreshForScheduling(_ context.Context, account *Account) (*Account, error) {
	copy := copyAccountForUserInfoQuotaRefresh(account)
	copy.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{{Limit: 10, WindowSpend: f.used, UsedKnown: true}}
	return copy, nil
}

func TestUSTCDefaultSchedulerRefreshesPreviouslyExhaustedWindow(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, true)
	repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	repo.accounts[0].Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 10, UsedKnown: true}}
	svc.ustcQuotaRefresher = ustcTestQuotaRefresher{used: 1}
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	selection.ReleaseFunc()
	svc.ustcQuotaRefresher = ustcTestQuotaRefresher{used: 10}
	_, err = svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestUSTCDefaultSchedulerBalancesInMixedGroupAndPreservesOtherStickyAccounts(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(3, true)
	repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	other := repo.accounts[0]
	other.ID = 4
	other.Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "test"}
	repo.accounts = append(repo.accounts, other)
	svc.accountRepo = repo
	ctx := context.Background()
	for i := 0; i < 60; i++ {
		selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "claude-code-session", "deepseek-flash", map[int64]struct{}{4: {}})
		require.NoError(t, err)
		selection.CommitRPMReservation()
		selection.ReleaseFunc()
	}
	counts, _ := rpm.GetRPMBatch(ctx, []int64{1, 2, 3})
	require.Equal(t, map[int64]int{1: 20, 2: 20, 3: 20}, counts)
	for id := range rpm.counts {
		rpm.counts[id] = 0
	}
	require.NoError(t, svc.cache.SetSessionAccountID(ctx, 0, "openai:other-session", 4, time.Hour))
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "other-session", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, int64(4), selection.Account.ID)
	require.False(t, selection.RPMReserved())
	selection.ReleaseFunc()
}

func TestUSTCDefaultSchedulerKeepsRefreshedQuotaDuringHydration(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, true)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 9.0
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{
		snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{1: &account},
	}}
	svc.ustcQuotaRefresher = ustcTestQuotaRefresher{used: 1}
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, "", userInfoQuotaSchedulingFailureReason(selection.Account, time.Now()))
	require.Equal(t, []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 1, UsedKnown: true}}, selection.Account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)])
	require.NotContains(t, account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows))
	selection.ReleaseFunc()
}

func TestUSTCQuotaHydrationKeepsNewestSnapshot(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name         string
		selectedAt   string
		hydratedAt   string
		wantSelected bool
	}{
		{"selected newer", now.Format(time.RFC3339), now.Add(-time.Second).Format(time.RFC3339), true},
		{"hydrated newer", now.Add(-time.Second).Format(time.RFC3339), now.Format(time.RFC3339), false},
		{"equal timestamp", now.Format(time.RFC3339), now.Format(time.RFC3339), false},
		{"selected unknown", "", now.Format(time.RFC3339), false},
		{"hydrated unknown", now.Format(time.RFC3339), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := ustcSchedulerFixture(1, true)
			selected := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
			selected.Extra = map[string]any{"upstream_userinfo_updated_at": tc.selectedAt, "upstream_userinfo_spend": 1.0,
				"upstream_userinfo_windows": []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 1, UsedKnown: true}}}
			hydrated := selected
			hydrated.Extra = map[string]any{"upstream_userinfo_updated_at": tc.hydratedAt, "upstream_userinfo_spend": 10.0,
				"upstream_userinfo_windows": []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 10, UsedKnown: true}}, "base_rpm": 20}
			svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: &hydrated}}}
			result, err := svc.hydrateSelectedAccount(context.Background(), &selected)
			require.NoError(t, err)
			wantSpend := 10.0
			if tc.wantSelected {
				wantSpend = 1.0
			}
			require.Equal(t, wantSpend, result.Extra["upstream_userinfo_spend"])
			require.Equal(t, 20, result.GetBaseRPM())
			require.Equal(t, 10.0, hydrated.Extra["upstream_userinfo_spend"], "hydration must not mutate the cache snapshot")
			require.Equal(t, 1.0, selected.Extra["upstream_userinfo_spend"])
			if !tc.wantSelected {
				require.Equal(t, "upstream_userinfo_window_exhausted", userInfoQuotaSchedulingFailureReason(result, now))
			}
		})
	}
}

func TestUSTCQuotaEligibilityAndExclusiveScope(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	now := time.Now()
	for _, tc := range []struct {
		name   string
		extra  map[string]any
		reason string
	}{
		{"unknown", nil, ""},
		{"invalid", map[string]any{"upstream_userinfo_valid": false}, "upstream_userinfo_invalid"},
		{"expired", map[string]any{"upstream_userinfo_expires_at": now.Add(-time.Hour).Format(time.RFC3339)}, "upstream_userinfo_expired"},
		{"serialized window", map[string]any{"upstream_userinfo_windows": []any{map[string]any{"limit": 10.0, "window_spend": 10.0, "used_known": true}}}, "upstream_userinfo_window_exhausted"},
		{"reset window", map[string]any{"upstream_userinfo_windows": []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 10, UsedKnown: true, ResetAt: now.Add(-time.Second).Format(time.RFC3339)}}}, ""},
		{"unknown usage", map[string]any{"upstream_userinfo_windows": []UserInfoBudgetWindow{{Limit: 10, UsedPercent: 100, UsedKnown: false}}}, ""},
		{"rounded remaining", map[string]any{"upstream_userinfo_windows": []UserInfoBudgetWindow{{Limit: 10, WindowSpend: 9.999, Remaining: 0, UsedKnown: true}}}, ""},
		{"scalar exhausted", map[string]any{"upstream_userinfo_max_budget": 10, "upstream_userinfo_spend": 10}, "upstream_userinfo_window_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account.Extra = tc.extra
			require.Equal(t, tc.reason, userInfoQuotaSchedulingFailureReason(&account, now))
		})
	}
	account.Extra = map[string]any{"base_rpm": 20}
	require.Equal(t, 4, account.GetRPMStickyBuffer())
	account.Extra["rpm_sticky_buffer"] = 5
	require.Equal(t, 5, account.GetRPMStickyBuffer())
	require.True(t, defaultUSTCAccountBalancingEnabled([]Account{account}))
	delete(account.Extra, "rpm_sticky_buffer")
	account.Credentials = map[string]any{"base_url": "https://api.openai.com"}
	account.Extra["upstream_userinfo_valid"] = false
	require.Equal(t, "", userInfoQuotaSchedulingFailureReason(&account, now))
	require.False(t, defaultUSTCAccountBalancingEnabled([]Account{account}))
	require.Equal(t, 20, account.GetRPMStickyBuffer())
}
