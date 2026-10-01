package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

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

func TestUSTCPreviousResponseKeepsItsKeyWhenFullInBothSchedulers(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint(advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc, cache := ustcSchedulerFixture(2, false)
			svc.cfg.RunMode = config.RunModeSimple
			svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced))
			accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
			cache.used[USTCKeyScope(&accounts[0])] = 20
			store := NewOpenAIWSStateStore(&stubGatewayCache{})
			svc.openaiWSStateStore = store
			ctx := context.Background()
			require.NoError(t, store.BindResponseAccount(ctx, 0, "resp_ustc", 1, time.Hour))
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "resp_ustc", "", "deepseek-flash", nil, OpenAIUpstreamTransportHTTPSSE, OpenAIEndpointCapabilityResponses, false, false, true)
			require.Nil(t, selection)
			_, capacity := USTCPoolRetryAfter(err)
			require.True(t, capacity)
			bound, err := store.GetResponseAccount(ctx, 0, "resp_ustc")
			require.NoError(t, err)
			require.Equal(t, int64(1), bound)
			value, _ := cache.USTCRead(ctx, USTCKeyScope(&accounts[1]), USTCLimits{20, 20})
			require.Zero(t, value.Pending)
		})
	}
}

func TestUSTCAutomaticParallelLimitOverridesOldManualDefault(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := &svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	account.Concurrency = 1
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.True(t, svc.PrepareUSTCAdmission(context.Background(), selection))
	require.Equal(t, 20, selection.Account.Concurrency)
	selection.ReleaseFunc()
}
