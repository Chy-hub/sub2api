package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCMetadataLimitsAreAutomaticAndDistinguishNullFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, metadata string
		known          bool
		limits         USTCLimits
	}{
		{"finite", `"rpm_limit":20,"max_parallel_requests":20,"tpm_limit":null`, true, USTCLimits{20, 20}},
		{"explicit-unlimited", `"rpm_limit":null,"max_parallel_requests":null,"tpm_limit":null`, true, USTCLimits{}},
		{"partial-unlimited", `"rpm_limit":20,"max_parallel_requests":null`, true, USTCLimits{20, 0}},
		{"missing-parallel", `"rpm_limit":20`, false, USTCLimits{}},
		{"zero", `"rpm_limit":0,"max_parallel_requests":20`, false, USTCLimits{}},
		{"negative", `"rpm_limit":-1,"max_parallel_requests":20`, false, USTCLimits{}},
		{"fraction", `"rpm_limit":20.5,"max_parallel_requests":20`, false, USTCLimits{}},
		{"invalid", `"rpm_limit":"bad","max_parallel_requests":20`, false, USTCLimits{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, repo := newUserInfoQuotaProbeFixture()
			upstream.bodies["/key/info"] = fmt.Sprintf(`{"info":{"max_budget":null,"spend":0,%s}}`, tc.metadata)
			account := userInfoQuotaTestAccount()
			account.Platform = PlatformOpenAI
			result, err := svc.QueryQuotaForAccount(context.Background(), account)
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, tc.known, result.LimitsKnown)
			account = overlayUserInfoQuotaSnapshot(account, repo.updated)
			limits, known := USTCAccountLimits(account)
			require.Equal(t, tc.known, known)
			if known {
				require.Equal(t, tc.limits, limits)
			}
		})
	}
}

func TestUSTCMetadataFailuresKeepLastValidLimits(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"omitted", `{"info":{"max_budget":null,"spend":0}}`, http.StatusOK},
		{"malformed", `{"info":{"max_budget":null,"spend":0,"rpm_limit":0,"max_parallel_requests":null}}`, http.StatusOK},
		{"http-failure", `{}`, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, _ := newUserInfoQuotaProbeFixture()
			upstream.bodies["/key/info"] = tc.body
			upstream.status = map[string]int{"/key/info": tc.status}
			account := userInfoQuotaTestAccount()
			account.Platform = PlatformOpenAI
			account.Extra = map[string]any{
				UserInfoQuotaExtraKey(UserInfoExtraSuffixLimitsKnown): true,
				UserInfoQuotaExtraKey(UserInfoExtraSuffixRPM):         20,
				UserInfoQuotaExtraKey(UserInfoExtraSuffixParallel):    20,
				UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated):     time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
			}
			refreshed, _ := svc.RefreshForScheduling(context.Background(), account)
			limits, known := USTCAccountLimits(refreshed)
			require.True(t, known)
			require.Equal(t, USTCLimits{20, 20}, limits)
		})
	}
}

func TestUSTCAdmissionFetchesLimitsEvenWhenOldBudgetSnapshotIsFresh(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	upstream.bodies["/key/info"] = `{"info":{"max_budget":null,"spend":0,"rpm_limit":20,"max_parallel_requests":20}}`
	account := userInfoQuotaTestAccount()
	account.Platform = PlatformOpenAI
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixValid):   true,
	}
	finished := make(chan struct{})
	svc.onRefresh = func() { close(finished) }
	gateway := &OpenAIGatewayService{ustcQuotaRefresher: svc}
	require.Nil(t, gateway.ustcAccountForAdmission(context.Background(), account), "unknown capacity cannot use old manual defaults")
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("a fresh monetary snapshot must not delay the first limits query")
	}
	ready := gateway.ustcAccountForAdmission(context.Background(), account)
	require.NotNil(t, ready)
	limits, known := USTCAccountLimits(ready)
	require.True(t, known)
	require.Equal(t, USTCLimits{20, 20}, limits)
	require.Equal(t, 20, ready.Concurrency)
	// Admission uses a copy; the old scheduler projection is never mutated.
	_, known = USTCAccountLimits(account)
	require.False(t, known)
}
