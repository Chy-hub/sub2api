package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestRefreshForSchedulingRefreshesMissingAndStaleSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name      string
		updatedAt time.Time
		wantCalls int
	}{
		{name: "missing", wantCalls: 2},
		{name: "stale", updatedAt: time.Now().Add(-time.Minute), wantCalls: 2},
		{name: "fresh", updatedAt: time.Now(), wantCalls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, _ := newUserInfoQuotaProbeFixture()
			account := userInfoQuotaTestAccount()
			if !tc.updatedAt.IsZero() {
				account.Extra = map[string]any{
					"preserve_me": "incoming",
					UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): tc.updatedAt.UTC().Format(time.RFC3339),
				}
				if tc.name == "fresh" {
					account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 4.5
				}
			}

			refreshed, err := svc.RefreshForScheduling(context.Background(), account)
			require.NoError(t, err)
			require.NotSame(t, account, refreshed)
			require.Equal(t, tc.wantCalls, len(upstream.calls))
			if tc.wantCalls == 0 {
				require.Equal(t, "incoming", refreshed.Extra["preserve_me"])
				require.Equal(t, 4.5, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
				return
			}
			require.Equal(t, 26.1032844, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
			require.NotContains(t, account.Extra, UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend), "input account must remain unchanged")
		})
	}
}

func TestRefreshForSchedulingCoalescesConcurrentCalls(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	account := userInfoQuotaTestAccount()
	const callers = 24
	var workers sync.WaitGroup
	errCh := make(chan error, callers)
	workers.Add(callers)
	for range callers {
		go func() {
			defer workers.Done()
			refreshed, err := svc.RefreshForScheduling(context.Background(), account)
			if err == nil && refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] != 26.1032844 {
				err = errors.New("unexpected quota snapshot")
			}
			errCh <- err
		}()
	}
	workers.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Equal(t, 2, len(upstream.calls), "all callers should share one /key/info plus /user/info refresh")
	require.Nil(t, account.Extra, "refresh calls must not modify the shared input account")
}

func TestRefreshForSchedulingRefreshesWhenExhaustedWindowResetHasPassed(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	account := userInfoQuotaTestAccount()
	now := time.Now().UTC()
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): now.Add(-10 * time.Second).Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{{
			Duration: "3h", Limit: 10, Remaining: 0, UsedPercent: 100, WindowSpend: 10, UsedKnown: true,
			ResetAt: now.Add(-time.Minute).Format(time.RFC3339),
		}},
	}

	refreshed, err := svc.RefreshForScheduling(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 2, len(upstream.calls), "an elapsed reset must invalidate even a fresh timestamp")
	require.NotEqual(t, 100.0, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)[0].UsedPercent)
	// The query snapshot replaces the exhausted cache only on the returned copy.
	require.Equal(t, 100.0, account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)[0].UsedPercent)
	_, err = svc.RefreshForScheduling(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 2, len(upstream.calls), "the completed refresh should satisfy repeated stale input")
}

func TestRefreshForSchedulingUsesCacheWithoutChangingLatestAccountFields(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	initial := userInfoQuotaTestAccount()
	initialWithSnapshot, err := svc.RefreshForScheduling(context.Background(), initial)
	require.NoError(t, err)
	cachedAt, ok := userInfoQuotaExtraUpdatedAt(initialWithSnapshot.Extra)
	require.True(t, ok)
	firstCallCount := len(upstream.calls)

	resetAt := time.Now().Add(time.Hour)
	latest := userInfoQuotaTestAccount()
	latest.Name = "latest name"
	latest.Status = StatusDisabled
	latest.RateLimitResetAt = &resetAt
	latest.Credentials["api_key"] = "sk-latest-credential"
	latest.Extra = map[string]any{
		"application_value": "latest",
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): cachedAt.Add(-time.Second).Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   999.0,
	}
	require.True(t, userInfoQuotaExtraIsFresh(latest.Extra, time.Now()), "incoming snapshot should also be within its freshness window")

	refreshed, err := svc.RefreshForScheduling(context.Background(), latest)
	require.NoError(t, err)
	require.Equal(t, firstCallCount, len(upstream.calls), "fresh per-account cache should avoid another upstream query")
	require.NotSame(t, latest, refreshed)
	require.Equal(t, "sk-latest-credential", refreshed.GetCredential("api_key"))
	require.Equal(t, latest.Name, refreshed.Name)
	require.Equal(t, latest.Status, refreshed.Status)
	require.Same(t, latest.RateLimitResetAt, refreshed.RateLimitResetAt)
	require.Equal(t, "latest", refreshed.Extra["application_value"])
	require.Equal(t, 26.1032844, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
	require.Equal(t, true, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixValid)])
	updatedAt, ok := userInfoQuotaExtraUpdatedAt(refreshed.Extra)
	require.True(t, ok)
	require.WithinDuration(t, time.Now(), updatedAt, userInfoQuotaSchedulingFreshness)
	require.Equal(t, 999.0, latest.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)], "the input snapshot must not be overwritten")

	refreshed.Extra["application_value"] = "modified return value"
	refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)[0].Duration = "mutated"
	require.Equal(t, "latest", latest.Extra["application_value"])

	otherLatest := userInfoQuotaTestAccount()
	otherLatest.Extra = map[string]any{UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): time.Now().Add(-time.Minute).Format(time.RFC3339)}
	refreshedAgain, err := svc.RefreshForScheduling(context.Background(), otherLatest)
	require.NoError(t, err)
	require.Equal(t, "3h", refreshedAgain.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)[0].Duration,
		"mutating one return value must not alter the cached snapshot")
}

func TestUserInfoQuotaOverlayDoesNotRegressNewerSnapshotOnFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	account := userInfoQuotaTestAccount()
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): now.Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   10.0,
	}
	fallback := map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): now.Add(-time.Second).Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   1.0,
	}
	result := overlayUserInfoQuotaSnapshot(copyAccountForUserInfoQuotaRefresh(account), fallback)
	require.Equal(t, 10.0, result.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
	require.Equal(t, 10.0, account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
}

func TestRefreshForSchedulingPreservesLastSnapshotAndThrottlesFailures(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	account := userInfoQuotaTestAccount()
	_, err := svc.RefreshForScheduling(context.Background(), account)
	require.NoError(t, err)
	priorCalls := len(upstream.calls)
	svc.refreshMu.Lock()
	svc.refreshCache[account.ID].successAt = time.Now().Add(-time.Minute)
	svc.refreshMu.Unlock()

	upstream.status = map[string]int{"/key/info": 503}
	latest := userInfoQuotaTestAccount()
	latest.Credentials["api_key"] = "sk-latest-credential"
	latest.Status = StatusDisabled
	latest.Extra = map[string]any{
		"other_extra": "keep",
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): time.Now().Add(-time.Minute).Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   999.0,
	}

	refreshed, err := svc.RefreshForScheduling(context.Background(), latest)
	require.Error(t, err)
	require.NotNil(t, refreshed)
	require.Equal(t, 26.1032844, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)], "failure must preserve and overlay the last successful snapshot")
	require.Equal(t, "keep", refreshed.Extra["other_extra"])
	require.Equal(t, "sk-latest-credential", refreshed.GetCredential("api_key"))
	require.Equal(t, StatusDisabled, refreshed.Status)
	require.Equal(t, 999.0, latest.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
	require.Equal(t, priorCalls+1, len(upstream.calls))

	admissionCopy, ready := svc.QuotaForAdmission(context.Background(), latest)
	require.True(t, ready, "a last successful snapshot is reusable during the failure backoff")
	require.Equal(t, 26.1032844, admissionCopy.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])

	_, retryErr := svc.RefreshForScheduling(context.Background(), latest)
	require.Error(t, retryErr)
	require.Equal(t, priorCalls+1, len(upstream.calls), "failure backoff should suppress an immediate retry")

	upstream.status = nil
	svc.refreshMu.Lock()
	if entry := svc.refreshCache[latest.ID]; entry != nil {
		entry.lastAttempt = time.Now().Add(-userInfoQuotaSchedulingBackoff - time.Second)
	}
	svc.refreshMu.Unlock()

	refreshed, err = svc.RefreshForScheduling(context.Background(), latest)
	require.NoError(t, err)
	require.Equal(t, priorCalls+3, len(upstream.calls), "a later request should retry after the failure backoff")
	require.Equal(t, 26.1032844, refreshed.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
}

func TestRefreshForSchedulingIgnoresNonUSTCAccountsAndHandlesNilService(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	account := userInfoQuotaTestAccount()
	account.Credentials["base_url"] = "https://example.com"

	refreshed, err := svc.RefreshForScheduling(context.Background(), account)
	require.NoError(t, err)
	require.NotSame(t, account, refreshed)
	require.Empty(t, upstream.calls)

	var nilService *UpstreamUserInfoQuotaService
	account = userInfoQuotaTestAccount()
	refreshed, err = nilService.RefreshForScheduling(context.Background(), account)
	require.Error(t, err)
	require.NotNil(t, refreshed)
	require.NotSame(t, account, refreshed)
}

func TestUserInfoQuotaRefreshCacheStaysBoundedAndPrunesIdleEntries(t *testing.T) {
	svc := &UpstreamUserInfoQuotaService{}
	now := time.Now().UTC()
	svc.refreshMu.Lock()
	for id := int64(1); id <= userInfoQuotaSchedulingCacheMax; id++ {
		svc.getRefreshCacheEntryLocked(id, now.Add(-time.Minute), true)
	}
	svc.refreshMu.Unlock()

	svc.refreshMu.Lock()
	svc.getRefreshCacheEntryLocked(userInfoQuotaSchedulingCacheMax+1, now, true)
	svc.refreshMu.Unlock()
	require.Len(t, svc.refreshCache, userInfoQuotaSchedulingCacheMax)
	require.NotContains(t, svc.refreshCache, int64(1), "least recently used entry should be evicted at capacity")
	require.Contains(t, svc.refreshCache, int64(userInfoQuotaSchedulingCacheMax+1))

	svc.refreshMu.Lock()
	svc.refreshCache[2].lastAccessAt = now.Add(-userInfoQuotaSchedulingCacheIdle - time.Second)
	svc.getRefreshCacheEntryLocked(userInfoQuotaSchedulingCacheMax+1, now, true)
	svc.refreshMu.Unlock()
	require.NotContains(t, svc.refreshCache, int64(2), "idle entries should be pruned during cache access")
}

func TestUSTCQuotaAdmissionUsesExactKnownSpend(t *testing.T) {
	now := time.Now().UTC()
	account := userInfoQuotaTestAccount()
	account.Platform = PlatformOpenAI
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{{
			Duration: "3h", Limit: 10, WindowSpend: 9.999, UsedPercent: 100, UsedKnown: true,
			ResetAt: now.Add(time.Hour).Format(time.RFC3339),
		}},
	}
	require.Empty(t, userInfoQuotaSchedulingFailureReason(account, now), "rounded 100% must not exhaust a window below its exact limit")
	require.False(t, userInfoQuotaResetElapsed(account.Extra, now), "rounded 100% must not trigger an early reset refresh")

	windows := account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)
	windows[0].WindowSpend = 10
	windows[0].UsedKnown = false
	require.Empty(t, userInfoQuotaSchedulingFailureReason(account, now), "unknown usage cannot block admission even when rounded percentage reaches 100")
	require.False(t, userInfoQuotaResetElapsed(account.Extra, now))
	windows[0].UsedKnown = true
	require.Equal(t, "upstream_userinfo_window_exhausted", userInfoQuotaSchedulingFailureReason(account, now))
	require.False(t, userInfoQuotaResetElapsed(account.Extra, now))
	windows[0].ResetAt = now.Add(-time.Second).Format(time.RFC3339)
	require.Empty(t, userInfoQuotaSchedulingFailureReason(account, now), "an exhausted window after its reset is eligible")
	require.True(t, userInfoQuotaResetElapsed(account.Extra, now))
}

func TestUSTCQuotaScalarBudgetFallbackUsesExactSpendAndReset(t *testing.T) {
	now := time.Now().UTC()
	account := userInfoQuotaTestAccount()
	account.Platform = PlatformOpenAI
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget): 10.0,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):  9.999,
	}
	require.Empty(t, userInfoQuotaSchedulingFailureReason(account, now), "scalar spend below the exact budget remains eligible")

	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 10.0
	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixResetAt)] = now.Add(time.Minute).Format(time.RFC3339)
	require.Equal(t, "upstream_userinfo_window_exhausted", userInfoQuotaSchedulingFailureReason(account, now), "precise scalar exhaustion blocks until reset")
	require.False(t, userInfoQuotaResetElapsed(account.Extra, now))

	account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixResetAt)] = now.Add(-time.Second).Format(time.RFC3339)
	require.Empty(t, userInfoQuotaSchedulingFailureReason(account, now), "an elapsed scalar reset restores eligibility")
	require.True(t, userInfoQuotaResetElapsed(account.Extra, now), "a scalar reset must also refresh an otherwise fresh snapshot")
}

func TestQuotaForAdmissionRetainsUnknownUsagePolicyAfterProbeFailure(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	upstream.status = map[string]int{"/key/info": http.StatusServiceUnavailable}
	account := userInfoQuotaTestAccount()
	account.Platform = PlatformOpenAI
	_, err := svc.RefreshForScheduling(context.Background(), account)
	require.Error(t, err)
	refreshed, ready := svc.QuotaForAdmission(context.Background(), account)
	require.True(t, ready, "a completed probe failure must not turn unknown usage into a hard veto")
	require.Empty(t, userInfoQuotaSchedulingFailureReason(refreshed, time.Now()))
	require.Len(t, upstream.calls, 1, "admission must reuse the probe failure's backoff")
}

func TestQuotaForAdmissionReturnsImmediatelyAndCoalescesRefresh(t *testing.T) {
	upstream := newBlockingUserInfoQuotaUpstream()
	repo := &userInfoQuotaRepoStub{}
	svc := NewUpstreamUserInfoQuotaService(repo, nil, upstream, nil)
	defer func() {
		select {
		case <-upstream.releaseKeyInfo:
		default:
			close(upstream.releaseKeyInfo)
		}
	}()
	account := userInfoQuotaTestAccount()
	startedAt := time.Now()
	firstCopy, ready := svc.QuotaForAdmission(context.Background(), account)
	require.False(t, ready)
	require.NotSame(t, account, firstCopy)
	require.Less(t, time.Since(startedAt), time.Second, "admission must not wait for the blocked upstream probe")
	select {
	case <-upstream.keyInfoStarted:
	case <-time.After(time.Second):
		t.Fatal("background quota refresh did not start")
	}

	secondAccount := copyAccountForUserInfoQuotaRefresh(account)
	secondAccount.Credentials["api_key"] = "sk-second-caller"
	secondCopy, secondReady := svc.QuotaForAdmission(context.Background(), secondAccount)
	require.False(t, secondReady)
	require.NotSame(t, secondAccount, secondCopy)
	require.Equal(t, int32(1), upstream.calls.Load(), "concurrent cold admissions must join the same account refresh")
	require.Nil(t, account.Extra)
	synchronousResult := make(chan error, 1)
	go func() {
		_, err := svc.RefreshForScheduling(context.Background(), account)
		synchronousResult <- err
	}()

	close(upstream.releaseKeyInfo)
	select {
	case <-upstream.refreshFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("background quota refresh did not complete")
	}
	select {
	case err := <-synchronousResult:
		require.NoError(t, err, "scheduling refresh should join admission's refresh flight")
	case <-time.After(2 * time.Second):
		t.Fatal("synchronous refresh did not join the admission flight")
	}
	waitForUserInfoQuotaSnapshot(t, svc, account.ID)

	readyCopy, ready := svc.QuotaForAdmission(context.Background(), account)
	require.True(t, ready)
	require.Equal(t, 26.1032844, readyCopy.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
	require.Nil(t, account.Extra, "admission refresh must overlay only a copy")
}

func TestQuotaForAdmissionFreshInputAndNewerCacheAreReady(t *testing.T) {
	svc, upstream, _ := newUserInfoQuotaProbeFixture()
	account := userInfoQuotaTestAccount()
	account.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): time.Now().Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   4.5,
	}
	result, ready := svc.QuotaForAdmission(context.Background(), account)
	require.True(t, ready)
	require.Equal(t, 4.5, result.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
	require.Empty(t, upstream.calls, "a fresh incoming snapshot needs no query")

	_, err := svc.RefreshForScheduling(context.Background(), userInfoQuotaTestAccount())
	require.NoError(t, err)
	older := userInfoQuotaTestAccount()
	older.Extra = map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated): time.Now().Add(-time.Second).Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):   999.0,
	}
	result, ready = svc.QuotaForAdmission(context.Background(), older)
	require.True(t, ready)
	require.Equal(t, 26.1032844, result.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)], "a newer fresh cache must not be regressed by a stale scheduler projection")
	require.Equal(t, 999.0, older.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)])
}

type blockingUserInfoQuotaUpstream struct {
	calls            atomic.Int32
	keyInfoStarted   chan struct{}
	releaseKeyInfo   chan struct{}
	refreshFinished  chan struct{}
	keyInfoStartOnce sync.Once
	refreshDoneOnce  sync.Once
}

func newBlockingUserInfoQuotaUpstream() *blockingUserInfoQuotaUpstream {
	return &blockingUserInfoQuotaUpstream{
		keyInfoStarted:  make(chan struct{}),
		releaseKeyInfo:  make(chan struct{}),
		refreshFinished: make(chan struct{}),
	}
}

func (u *blockingUserInfoQuotaUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls.Add(1)
	var body string
	if req.URL.Path == "/key/info" {
		u.keyInfoStartOnce.Do(func() { close(u.keyInfoStarted) })
		<-u.releaseKeyInfo
		body = userInfoQuotaKeyInfoFixture
	} else {
		body = userInfoQuotaUserInfoFixture
		u.refreshDoneOnce.Do(func() { close(u.refreshFinished) })
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func (u *blockingUserInfoQuotaUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func waitForUserInfoQuotaSnapshot(t *testing.T, svc *UpstreamUserInfoQuotaService, accountID int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		svc.refreshMu.Lock()
		entry := svc.refreshCache[accountID]
		ready := entry != nil && !entry.successAt.IsZero()
		svc.refreshMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("quota refresh cache was not populated")
}
