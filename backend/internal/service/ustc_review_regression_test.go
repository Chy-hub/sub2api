//go:build unit

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUSTCAdvancedFastPathRespectsChannelPricingRestriction(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc, cache := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
	svc.channelService = newTestChannelService(makeStandardRepo(Channel{
		ID: 1, Status: StatusActive, GroupIDs: []int64{10}, RestrictModels: true,
		BillingModelSource: BillingModelSourceChannelMapped,
		ModelPricing:       []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{"gpt-4o"}}},
	}, map[int64]string{10: PlatformOpenAI}))
	groupID := int64(10)
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(context.Background(), &groupID, "", "", "deepseek-flash", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, false)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Contains(t, err.Error(), "channel pricing restriction")
	require.Empty(t, cache.tickets, "restricted requests must never reserve Key capacity")
}

type ustcBackgroundOnlyRefresher struct {
	syncCalls int
	contexts  []bool
}

func (r *ustcBackgroundOnlyRefresher) RefreshForScheduling(_ context.Context, account *Account) (*Account, error) {
	r.syncCalls++
	return account, nil
}

func (r *ustcBackgroundOnlyRefresher) QuotaForAdmission(ctx context.Context, account *Account) (*Account, bool) {
	background, _ := ctx.Value(ustcQuotaBackgroundAdmissionKey{}).(bool)
	r.contexts = append(r.contexts, background)
	return copyAccountForUserInfoQuotaRefresh(account), background
}

func TestUSTCAdvancedCandidateRechecksKeepBackgroundRefreshContext(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	account.Extra[UserInfoQuotaExtraKey("updated_at")] = time.Now().Add(-time.Minute).Format(time.RFC3339)
	refresher := &ustcBackgroundOnlyRefresher{}
	svc.ustcQuotaRefresher = refresher
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	selection, _, err := scheduler.tryAcquireOpenAISelectionOrder(context.Background(), OpenAIAccountScheduleRequest{
		Platform: PlatformOpenAI, RequestedModel: "deepseek-flash", RequiredTransport: OpenAIUpstreamTransportHTTPSSE, RequiredCapability: OpenAIEndpointCapabilityResponses,
	}, []openAIAccountCandidateScore{{account: &account}})
	require.NoError(t, err)
	require.NotNil(t, selection, "a stale healthy USTC candidate must not disappear during advanced rechecks")
	defer selection.ReleaseFunc()
	require.Zero(t, refresher.syncCalls)
	require.GreaterOrEqual(t, len(refresher.contexts), 3)
	for _, background := range refresher.contexts {
		require.True(t, background)
	}
}

type ustcChangingWaitCountCache struct {
	schedulerTestConcurrencyCache
	calls int
}

func (c *ustcChangingWaitCountCache) GetAccountWaitingCount(context.Context, int64) (int, error) {
	c.calls++
	if c.calls == 1 {
		return 3, nil
	}
	return 0, nil
}

func TestUSTCMixedPoolFallbackRestoresNonUSTCStickyWaitPlan(t *testing.T) {
	svc, cache := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	accounts[1].Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "test"}
	cache.used[USTCKeyScope(&accounts[0])] = 20
	svc.cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	svc.cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 30 * time.Second
	concurrency := &ustcChangingWaitCountCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{acquireResults: map[int64]bool{2: false}}}
	svc.concurrencyService = NewConcurrencyService(concurrency)
	ctx := context.Background()
	require.NoError(t, svc.cache.SetSessionAccountID(ctx, 0, "openai:session", 2, time.Hour))
	selection, err := svc.selectBalancedDefaultUSTCAccount(ctx, nil, accounts, "session", "deepseek-flash", nil, false, OpenAIEndpointCapabilityResponses, false, OpenAIUpstreamTransportAny)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.True(t, selection.stickySessionHit)
	require.Equal(t, 45*time.Second, selection.WaitPlan.Timeout)
	require.Equal(t, 3, selection.WaitPlan.MaxWaiting)
	require.Equal(t, 2, concurrency.calls, "final fallback must reconsider a newly freed sticky queue")
}

type ustcDispatchReadRepo struct {
	schedulerTestOpenAIAccountRepo
	gets atomic.Int64
}

func (r *ustcDispatchReadRepo) GetByID(context.Context, int64) (*Account, error) {
	r.gets.Add(1)
	return nil, nil // Simulate a row changing while its scheduling snapshot is ready.
}

func TestUSTCPoolDispatchCoalescesDBRechecksAcrossWaiters(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 100 * time.Millisecond
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	account := base.accounts[0]
	repo := &ustcDispatchReadRepo{schedulerTestOpenAIAccountRepo: base}
	svc.accountRepo = repo
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: &account}}}
	state := svc.defaultUSTCPoolState()
	coordinator := &ustcPoolCoordinator{service: svc, state: state, scope: "group_0", wake: make(chan struct{}, 1)}
	ctx := context.WithValue(context.Background(), ustcBoundAccountKey{}, true)
	jobs := make([]*ustcPoolJob, 100)
	for i := range jobs {
		jobs[i] = &ustcPoolJob{ctx: ctx, accounts: []Account{account}, model: "deepseek-flash", capability: OpenAIEndpointCapabilityResponses, result: make(chan ustcPoolResult, 1)}
	}
	// The coordinator compacts its queue in place; keep the waiter's iteration
	// independent from that mutable backing array.
	coordinator.jobs = append([]*ustcPoolJob(nil), jobs...)
	state.coordinators[coordinator.scope] = coordinator
	go coordinator.run()
	for _, job := range jobs {
		select {
		case result := <-job.result:
			require.Nil(t, result.selection)
			_, limited := USTCPoolRetryAfter(result.err)
			require.True(t, limited)
		case <-time.After(time.Second):
			t.Fatal("waiter did not finish within its bounded wait")
		}
	}
	require.EqualValues(t, 1, repo.gets.Load(), "one candidate row read per pass, not one per waiter")
}

func TestUSTCBindingDetectionDoesNotAddNonUSTCAccountReads(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	base.accounts[0].Credentials = map[string]any{"base_url": "https://api.example.com", "api_key": "synthetic"}
	repo := &ustcPoolCountingRepo{schedulerTestOpenAIAccountRepo: base}
	svc.accountRepo = repo
	store := NewOpenAIWSStateStore(&stubGatewayCache{})
	svc.openaiWSStateStore = store
	ctx := context.Background()
	require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_non_ustc", 1, time.Hour))
	id := svc.ResolveAccountIDByPreviousResponseIDForScheduler(ctx, nil, "resp_non_ustc", "deepseek-flash", nil, OpenAIEndpointCapabilityResponses, false)
	require.Equal(t, int64(1), id)
	require.Equal(t, 1, repo.gets, "shared raw binding load must not add a second non-USTC DB query")
}
