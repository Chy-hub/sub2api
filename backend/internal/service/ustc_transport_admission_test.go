//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUSTCPoolRefreshKeepsWebSocketTransportRequirement(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprint(advanced), func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			svc, cache := ustcSchedulerFixture(2, false)
			svc.cfg.RunMode = config.RunModeSimple
			svc.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(fmt.Sprint(advanced))
			base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
			base.accounts[0].Priority = -1
			base.accounts[0].Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
			base.accounts[1].Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeHTTPBridge
			svc.accountRepo = &ustcStablePoolRepo{schedulerTestOpenAIAccountRepo: base, pool: base.accounts}

			selection, _, err := svc.SelectAccountWithSchedulerForCapability(context.Background(), nil, "", "", "deepseek-flash", nil, OpenAIUpstreamTransportResponsesWebsocketV2Ingress, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			defer selection.ReleaseFunc()
			require.Equal(t, int64(2), selection.Account.ID, "pool supplementation must not restore an account with WS disabled")
			capacity, err := cache.USTCRead(context.Background(), USTCKeyScope(&base.accounts[0]), USTCLimits{20, 20})
			require.NoError(t, err)
			require.Zero(t, capacity.Pending)
		})
	}
}

func TestUSTCTransportIsRecheckedAfterAccountRefresh(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	svc.cfg.RunMode = config.RunModeSimple
	svc.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	base.accounts[0].Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeOff
	stale := copyAccountForUserInfoQuotaRefresh(&base.accounts[0])
	stale.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeHTTPBridge
	svc.schedulerSnapshot = &SchedulerSnapshotService{cache: &openAISnapshotCacheStub{accountsByID: map[int64]*Account{1: stale}}}

	selection, err := svc.selectBalancedDefaultUSTCAccount(context.Background(), nil, []Account{*stale}, "", "deepseek-flash", nil, false, OpenAIEndpointCapabilityResponses, false, OpenAIUpstreamTransportResponsesWebsocketV2Ingress)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Empty(t, cache.tickets)
}

func TestUSTCQuotaNotReadyWithPrivacyRequirementDoesNotPanic(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	svc.ustcQuotaRefresher = &ustcBackgroundOnlyRefresher{}
	ctx := context.WithValue(context.Background(), ustcQuotaAdmissionKey{}, true)
	ctx = context.WithValue(ctx, openAIGroupPrivacyRequirementContextKey{}, openAIGroupPrivacyRequirement{required: true})
	require.Nil(t, svc.recheckSelectedOpenAIAccountFromDB(ctx, &account, nil, PlatformOpenAI, "deepseek-flash", false, OpenAIEndpointCapabilityResponses))
}
