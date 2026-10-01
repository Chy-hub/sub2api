//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type ustc429RepoStub struct {
	mockAccountRepoForGemini
	resets           []time.Time
	updateExtraCalls int
}

func (r *ustc429RepoStub) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.resets = append(r.resets, resetAt)
	return nil
}

func (r *ustc429RepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	r.updateExtraCalls++
	return nil
}

type ustc429NeverShrinkRepoStub struct {
	*ustc429RepoStub
	setRateLimitedIfLaterCalls int
}

func (r *ustc429NeverShrinkRepoStub) SetRateLimitedIfLater(ctx context.Context, id int64, resetAt time.Time) error {
	r.setRateLimitedIfLaterCalls++
	if len(r.resets) > 0 && !resetAt.After(r.resets[len(r.resets)-1]) {
		return nil
	}
	return r.ustc429RepoStub.SetRateLimited(ctx, id, resetAt)
}

func ustc429Account(extra map[string]any) *Account {
	return &Account{
		ID:          8101,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"base_url": "https://api.llm.ustc.edu.cn/v1",
			"api_key":  "ustc-test-key",
		},
		Extra: extra,
	}
}

func runUSTC429(t *testing.T, account *Account, headers http.Header, body string, settingService *SettingService) (*ustc429RepoStub, *runtimeBlockRecorder, time.Time, time.Time) {
	t.Helper()
	repo := &ustc429RepoStub{}
	blocker := &runtimeBlockRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAccountRuntimeBlocker(blocker)
	svc.SetSettingService(settingService)
	before := time.Now()
	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(body))
	after := time.Now()
	return repo, blocker, before, after
}

func TestHandle429_USTCRPMUsesConservativeWindowAndSkipsCodexWindows(t *testing.T) {
	settingsRepo := newMockSettingRepo()
	encoded, err := json.Marshal(RateLimit429CooldownSettings{Enabled: true, CooldownSeconds: 7200})
	require.NoError(t, err)
	settingsRepo.data[SettingKeyRateLimit429CooldownSettings] = string(encoded)

	// This broad setting and Codex's reset are both inapplicable to USTC.
	settingService := NewSettingService(settingsRepo, &config.Config{})
	account := ustc429Account(map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{{
			Duration: "24h", Limit: 100, WindowSpend: 100, UsedKnown: true,
			ResetAt: time.Now().Add(6 * time.Hour).Format(time.RFC3339),
		}},
	})
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"100"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"604800"},
	}
	repo, blocker, before, after := runUSTC429(t, account, headers,
		`{"error":{"type":"rate_limit_error","message":"requests per minute exceeded"}}`, settingService)

	require.Len(t, repo.resets, 1)
	require.GreaterOrEqual(t, repo.resets[0], before.Add(ustcTransient429Cooldown))
	require.LessOrEqual(t, repo.resets[0], after.Add(ustcTransient429Cooldown))
	require.Zero(t, repo.updateExtraCalls, "USTC 429 must not persist a Codex snapshot")
	require.Len(t, blocker.until, 1)
	require.Equal(t, repo.resets[0], blocker.until[0])
}

func TestHandle429_USTCRetryAfterSecondsAndHTTPDateAreHonored(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header func(time.Time) string
	}{
		{name: "delta seconds", header: func(time.Time) string { return "47" }},
		{name: "http date", header: func(now time.Time) string { return now.Add(73 * time.Second).UTC().Format(http.TimeFormat) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headerValue := tc.header(time.Now())
			repo, _, before, after := runUSTC429(t, ustc429Account(nil), http.Header{"Retry-After": []string{headerValue}},
				`{"error":{"message":"too many requests"}}`, nil)
			require.Len(t, repo.resets, 1)
			var want time.Time
			if tc.name == "delta seconds" {
				want = before.Add(47 * time.Second)
			} else {
				want, _ = http.ParseTime(headerValue)
			}
			require.WithinDuration(t, want, repo.resets[0], time.Second)
			require.True(t, repo.resets[0].After(after), "future Retry-After must keep the account cooled")
		})
	}
}

func TestHandle429_USTCOrdinary429DoesNotShortenPriorRetryAfter(t *testing.T) {
	repo := &ustc429NeverShrinkRepoStub{ustc429RepoStub: &ustc429RepoStub{}}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := ustc429Account(nil)

	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests,
		http.Header{"Retry-After": []string{"147"}}, []byte(`{"error":{"message":"too many requests"}}`))
	require.Len(t, repo.resets, 1)
	retryAfterReset := repo.resets[0]
	require.True(t, retryAfterReset.After(time.Now().Add(140*time.Second)))

	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests,
		http.Header{}, []byte(`{"error":{"type":"rate_limit_error","message":"requests per minute exceeded"}}`))

	require.Equal(t, 2, repo.setRateLimitedIfLaterCalls, "both responses use the optional never-shrinking write")
	require.Len(t, repo.resets, 1, "the later ordinary 60-second cooldown must not overwrite the longer Retry-After")
	require.Equal(t, retryAfterReset, repo.resets[0])
	require.True(t, repo.resets[0].After(time.Now()))
}

func TestHandle429_USTCBudgetFutureWindowRechecksWithinThirtySeconds(t *testing.T) {
	now := time.Now()
	account := ustc429Account(map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{
			{Duration: "3h", Limit: 30, WindowSpend: 30, UsedKnown: true, ResetAt: now.Add(time.Hour).Format(time.RFC3339)},
			{Duration: "12h", Limit: 70, WindowSpend: 70, UsedKnown: true},
			{Duration: "24h", Limit: 100, WindowSpend: 100, UsedKnown: true, ResetAt: now.Add(6 * time.Hour).Format(time.RFC3339)},
		},
	})
	repo, blocker, before, after := runUSTC429(t, account, http.Header{},
		`{"error":{"type":"budget_exceeded","message":"Budget has been exceeded"}}`, nil)

	require.Len(t, repo.resets, 1)
	require.GreaterOrEqual(t, repo.resets[0], before.Add(ustcBudgetRecheckCooldown))
	require.LessOrEqual(t, repo.resets[0], after.Add(ustcBudgetRecheckCooldown))
	require.Equal(t, repo.resets[0], blocker.until[0])
}

func TestHandle429_USTCBudgetNearResetRechecksAtThatReset(t *testing.T) {
	nearReset := time.Now().Add(12 * time.Second).UTC().Truncate(time.Second)
	account := ustc429Account(map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{
			{Duration: "3h", Limit: 30, WindowSpend: 30, UsedKnown: true, ResetAt: nearReset.Format(time.RFC3339)},
			{Duration: "24h", Limit: 100, WindowSpend: 100, UsedKnown: true, ResetAt: time.Now().Add(6 * time.Hour).Format(time.RFC3339)},
		},
	})
	repo, _, _, _ := runUSTC429(t, account, http.Header{},
		`{"error":{"code":"budget_exceeded"}}`, nil)

	require.Len(t, repo.resets, 1)
	require.WithinDuration(t, nearReset, repo.resets[0], time.Second)
}

func TestHandle429_USTCBudgetErrorWithoutResetStillHonorsRetryAfter(t *testing.T) {
	account := ustc429Account(nil)
	repo, _, before, after := runUSTC429(t, account, http.Header{"Retry-After": []string{"91"}},
		`{"error":{"code":"budget_exceeded","message":"No remaining budget"}}`, nil)

	require.Len(t, repo.resets, 1)
	require.GreaterOrEqual(t, repo.resets[0], before.Add(91*time.Second))
	require.LessOrEqual(t, repo.resets[0], after.Add(91*time.Second))
}

func TestHandle429_USTCBudgetErrorWithoutAnyResetGetsBriefCooldown(t *testing.T) {
	repo, _, before, after := runUSTC429(t, ustc429Account(nil), http.Header{},
		`{"error":{"code":"budget_exceeded","message":"Budget has been exceeded"}}`, nil)

	require.Len(t, repo.resets, 1)
	require.GreaterOrEqual(t, repo.resets[0], before.Add(ustcBudgetRecheckCooldown))
	require.LessOrEqual(t, repo.resets[0], after.Add(ustcBudgetRecheckCooldown))
}

func TestHandle429_USTCBudgetRetryAfterDoesNotExtendToWindowReset(t *testing.T) {
	account := ustc429Account(map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows): []UserInfoBudgetWindow{{
			Duration: "24h", Limit: 100, WindowSpend: 100, UsedKnown: true,
			ResetAt: time.Now().Add(6 * time.Hour).Format(time.RFC3339),
		}},
	})
	repo, _, before, after := runUSTC429(t, account, http.Header{"Retry-After": []string{"47"}},
		`{"error":{"code":"budget_exceeded"}}`, nil)

	require.Len(t, repo.resets, 1)
	require.GreaterOrEqual(t, repo.resets[0], before.Add(47*time.Second))
	require.LessOrEqual(t, repo.resets[0], after.Add(47*time.Second))
}

func TestHandle429_USTCBudgetClassifierDoesNotTreatGenericRPMAsBudget(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"error":{"code":"budget_exceeded"}}`, true},
		{`{"error":{"message":"Budget limit reached"}}`, true},
		{`{"error":{"message":"预算已用完"}}`, true},
		{`{"error":{"type":"rate_limit_error","message":"requests per minute exceeded"}}`, false},
		{`{"error":{"message":"quota exceeded"}}`, false},
		{`{"error":{"message":"The budget is $10; request limit has been reached"}}`, false},
	} {
		require.Equal(t, tc.want, isUSTCBudgetExhaustion429([]byte(tc.body)), tc.body)
	}
}

func TestHandle429_NonUSTCOpenAIStillUsesExistingCodexReset(t *testing.T) {
	account := ustc429Account(nil)
	account.Credentials["base_url"] = "https://api.openai.com/v1"
	repo := &ustc429RepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	headers := http.Header{
		"X-Codex-Primary-Used-Percent":        []string{"100"},
		"X-Codex-Primary-Reset-After-Seconds": []string{"3600"},
		"X-Codex-Primary-Window-Minutes":      []string{"300"},
	}
	before := time.Now()
	svc.handle429(context.Background(), account, headers, nil)
	require.Len(t, repo.resets, 1)
	require.WithinDuration(t, before.Add(time.Hour), repo.resets[0], 2*time.Second)
}
