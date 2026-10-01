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

type ustcLegacyRPMCache struct{ RPMCache }

func TestUSTCLegacyCacheCountsOnlyCommittedAdmission(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	svc.rpmCache = ustcLegacyRPMCache{RPMCache: rpm}
	ctx := context.Background()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	count, _ := rpm.GetRPM(ctx, 1)
	require.Zero(t, count, "a cache without refunds must not count an abandoned selection")
	selection.ReleaseFunc()
	selection.CommitRPMReservation()
	count, _ = rpm.GetRPM(ctx, 1)
	require.Zero(t, count)

	selection, err = svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); selection.CommitRPMReservation() }()
	}
	wg.Wait()
	selection.ReleaseFunc()
	count, _ = rpm.GetRPM(ctx, 1)
	require.Equal(t, 1, count, "successful admission commits once even with an older cache")
}

func TestUSTCPreviousResponseChecksQuotaBeforeAcquiringSlot(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("advanced_%t", advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc, rpm := ustcSchedulerFixture(1, false)
			svc.cfg.RunMode = config.RunModeSimple
			svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced))
			svc.ustcQuotaRefresher = ustcTestQuotaRefresher{used: 10}
			var acquired []int64
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired})
			store := NewOpenAIWSStateStore(&stubGatewayCache{})
			svc.openaiWSStateStore = store
			ctx := context.Background()
			require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_ustc", 1, time.Hour))
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "resp_ustc", "", "deepseek-flash", nil,
				OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.Nil(t, selection)
			require.Empty(t, acquired, "exhausted response bindings must be rejected before acquiring a slot")
			bound, err := store.GetResponseAccount(ctx, 0, "resp_ustc")
			require.NoError(t, err)
			require.Equal(t, int64(1), bound, "a transient quota veto must preserve the response binding")
			count, _ := rpm.GetRPM(ctx, 1)
			require.Zero(t, count)
		})
	}
}

func TestUSTCDefaultPreviousResponseUsesRPMAdmission(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc, rpm := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("false")
	svc.ustcQuotaRefresher = ustcTestQuotaRefresher{used: 1}
	store := NewOpenAIWSStateStore(&stubGatewayCache{})
	svc.openaiWSStateStore = store
	ctx := context.Background()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_ustc", 1, time.Hour))
	for range 25 {
		selection, decision, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "resp_ustc", "", "deepseek-flash", nil,
			OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
		require.NoError(t, err)
		require.True(t, decision.StickyPreviousHit)
		require.True(t, selection.defaultRPMManaged)
		require.True(t, svc.PrepareDefaultAccountRPM(ctx, selection))
		selection.CommitRPMReservation()
		selection.ReleaseFunc()
	}
	count, _ := rpm.GetRPM(ctx, 1)
	require.Equal(t, 25, count)
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "resp_ustc", "", "deepseek-flash", nil,
		OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
	if err == nil {
		require.False(t, svc.PrepareDefaultAccountRPM(ctx, selection))
		selection.ReleaseFunc()
	} else {
		require.ErrorIs(t, err, ErrNoAvailableAccounts)
	}
	count, _ = rpm.GetRPM(ctx, 1)
	require.Equal(t, 25, count)
}

func TestUSTCDefaultSchedulerPreservesStickyWaitSettings(t *testing.T) {
	for _, waiting := range []int{0, 3} {
		t.Run(fmt.Sprintf("waiting_%d", waiting), func(t *testing.T) {
			svc, _ := ustcSchedulerFixture(1, true)
			svc.cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
			svc.cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
			svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
			svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
				acquireResults: map[int64]bool{1: false}, waitCounts: map[int64]int{1: waiting}})
			ctx := context.Background()
			require.NoError(t, svc.cache.SetSessionAccountID(ctx, 0, "openai:session", 1, time.Hour))
			selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "session", "deepseek-flash", nil)
			require.NoError(t, err)
			require.NotNil(t, selection.WaitPlan)
			require.True(t, selection.stickySessionHit)
			if waiting < 3 {
				require.Equal(t, 120*time.Second, selection.WaitPlan.Timeout)
				require.Equal(t, 3, selection.WaitPlan.MaxWaiting)
			} else {
				require.Equal(t, 30*time.Second, selection.WaitPlan.Timeout)
				require.Equal(t, 100, selection.WaitPlan.MaxWaiting)
			}
		})
	}
}

func TestUSTCMixedPoolPreservesNonUSTCStickyWaitAndSpillover(t *testing.T) {
	for _, waiting := range []int{0, 3} {
		t.Run(fmt.Sprintf("waiting_%d", waiting), func(t *testing.T) {
			svc, _ := ustcSchedulerFixture(2, true)
			repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
			other := repo.accounts[0]
			other.ID = 3
			other.Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "test"}
			repo.accounts = append(repo.accounts, other)
			svc.accountRepo = repo
			svc.cfg.Gateway.Scheduling.StickySessionWaitTimeout = 120 * time.Second
			svc.cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
			svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{
				acquireResults: map[int64]bool{3: false}, waitCounts: map[int64]int{3: waiting}})
			ctx := context.Background()
			require.NoError(t, svc.cache.SetSessionAccountID(ctx, 0, "openai:session", 3, time.Hour))
			selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "session", "deepseek-flash", nil)
			require.NoError(t, err)
			if waiting < 3 {
				require.Equal(t, int64(3), selection.Account.ID)
				require.NotNil(t, selection.WaitPlan, "non-USTC sticky wait must take precedence over free USTC slots")
				require.Equal(t, 120*time.Second, selection.WaitPlan.Timeout)
				require.True(t, selection.stickySessionHit)
			} else {
				require.NotEqual(t, int64(3), selection.Account.ID)
				require.True(t, selection.Acquired)
				selection.ReleaseFunc()
			}
			bound, err := svc.cache.GetSessionAccountID(ctx, 0, "openai:session")
			require.NoError(t, err)
			require.Equal(t, int64(3), bound, "temporary capacity spillover must retain the non-USTC binding")
		})
	}
}

type ustcPoolCountingRepo struct {
	schedulerTestOpenAIAccountRepo
	lists   int
	gets    int
	listErr error
}

func (r *ustcPoolCountingRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.gets++
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func (r *ustcPoolCountingRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	r.lists++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByPlatform(ctx, platform)
}

func TestUSTCPoolProbePreservesNonUSTCStickyFastPath(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	base.accounts[0].Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "test"}
	repo := &ustcPoolCountingRepo{schedulerTestOpenAIAccountRepo: base, listErr: fmt.Errorf("pool query unavailable")}
	svc.accountRepo = repo
	ctx := context.Background()
	require.NoError(t, svc.cache.SetSessionAccountID(ctx, 0, "openai:session", 1, time.Hour))
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "session", "deepseek-flash", nil)
	require.NoError(t, err, "new USTC detection must not make a healthy non-USTC sticky request depend on listing")
	require.True(t, selection.stickySessionHit)
	require.Zero(t, repo.lists)
	require.Equal(t, 1, repo.gets, "USTC detection must reuse the bound account instead of adding another query")
	selection.ReleaseFunc()
}

func TestUSTCPoolProbeReusesListOnLegacyFallback(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	base.accounts[0].Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "test"}
	repo := &ustcPoolCountingRepo{schedulerTestOpenAIAccountRepo: base}
	svc.accountRepo = repo
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.Equal(t, 1, repo.lists, "the USTC probe's list must be reused by legacy fallback")
	selection.ReleaseFunc()
}

type ustcCandidateQuotaRefresher struct {
	queried   []int64
	exhausted map[int64]bool
}

func (f *ustcCandidateQuotaRefresher) RefreshForScheduling(_ context.Context, account *Account) (*Account, error) {
	f.queried = append(f.queried, account.ID)
	used := 1.0
	if f.exhausted[account.ID] {
		used = 10
	}
	return ustcTestQuotaRefresher{used: used}.RefreshForScheduling(context.Background(), account)
}

func TestUSTCLegacyCandidateRankingProbesOnlyUntilEligible(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_exhausted_%t", exhausted), func(t *testing.T) {
			svc, _ := ustcSchedulerFixture(10, false)
			accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
			for i := range accounts {
				accounts[i].Priority = i
			}
			refresher := &ustcCandidateQuotaRefresher{exhausted: map[int64]bool{1: exhausted}}
			svc.ustcQuotaRefresher = refresher
			account, _, _ := svc.selectBestAccount(context.Background(), nil, PlatformOpenAI, accounts, "deepseek-flash", nil, false, "", false)
			require.NotNil(t, account)
			if exhausted {
				require.Equal(t, int64(2), account.ID)
				require.Equal(t, []int64{1, 2}, refresher.queried)
			} else {
				require.Equal(t, int64(1), account.ID)
				require.Equal(t, []int64{1}, refresher.queried)
			}
		})
	}
}

type ustcAdmissionOnlyQuotaRefresher struct{}

func (ustcAdmissionOnlyQuotaRefresher) RefreshForScheduling(context.Context, *Account) (*Account, error) {
	panic("admission must not synchronously probe an upstream while holding a slot")
}
func (ustcAdmissionOnlyQuotaRefresher) QuotaForAdmission(_ context.Context, account *Account) (*Account, bool) {
	return copyAccountForUserInfoQuotaRefresh(account), false
}

func TestUSTCAdmissionUsesNonblockingQuotaReader(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	svc.ustcQuotaRefresher = ustcAdmissionOnlyQuotaRefresher{}
	require.False(t, svc.PrepareDefaultAccountRPM(context.Background(), selection))
	selection.ReleaseFunc()
	count, _ := rpm.GetRPM(context.Background(), 1)
	require.Zero(t, count)
}

func TestUSTCAPIKeyCannotEnterLiveRouting(t *testing.T) {
	svc, rpm := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	account.Credentials["openai_capabilities"] = []string{"live"}
	require.False(t, account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityLive))
	selection, err := svc.selectAccountWithLoadAwareness(context.Background(), nil, PlatformOpenAI, "", "deepseek-flash", nil, false, OpenAIEndpointCapabilityLive, false)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	count, _ := rpm.GetRPM(context.Background(), 1)
	require.Zero(t, count)
}
