package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// 合成夹具：形状对齐 USTC /key/info + /user/info（key 级 max_budget 为 null、
// 24h 挂在 user_info 上），数值为虚构，不含真实 key / 用户标识。
const (
	userInfoQuotaKeyInfoFixture = `{
		"key": "0000000000000000000000000000000000000000000000000000000000000000",
		"info": {
			"key_alias": "SYNTH_KEY_ALIAS",
			"spend": 26.1032844,
			"expires": "2054-02-07T11:18:01.178+08:00",
			"blocked": null,
			"max_budget": null,
			"budget_duration": null,
			"budget_reset_at": null,
			"budget_limits": [
				{"reset_at": "2026-09-27T00:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"},
				{"reset_at": "2026-09-27T00:00:00+08:00", "max_budget": 70.0, "budget_duration": "12h"}
			],
			"budget_limits_usage": {
				"3h": {"current_spend": 0.3907},
				"12h": {"current_spend": 0.3907}
			}
		}
	}`

	userInfoQuotaUserInfoFixture = `{
		"user_id": "00000000-0000-0000-0000-000000000000",
		"user_info": {
			"user_alias": "SYNTH_USER",
			"max_budget": 100.0,
			"spend": 0.390654,
			"budget_duration": "24h",
			"budget_reset_at": "2026-09-26T16:00:00Z"
		},
		"keys": []
	}`
)

// userInfoQuotaUpstreamStub 按请求路径返回固定响应体，并记录访问过的路径。
type userInfoQuotaUpstreamStub struct {
	bodies map[string]string
	status map[string]int
	calls  []string
}

func (s *userInfoQuotaUpstreamStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls = append(s.calls, req.URL.Path)
	status := s.status[req.URL.Path]
	if status == 0 {
		status = http.StatusOK
	}
	body := s.bodies[req.URL.Path]
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}, nil
}

func (s *userInfoQuotaUpstreamStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return s.Do(req, proxyURL, accountID, accountConcurrency)
}

// userInfoQuotaRepoStub 只关心 UpdateExtra；其余接口方法用不到，靠嵌入接口占位。
type userInfoQuotaRepoStub struct {
	AccountRepository
	updated map[string]any
}

func (r *userInfoQuotaRepoStub) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updated = updates
	return nil
}

func newUserInfoQuotaProbeFixture() (*UpstreamUserInfoQuotaService, *userInfoQuotaUpstreamStub, *userInfoQuotaRepoStub) {
	upstream := &userInfoQuotaUpstreamStub{bodies: map[string]string{
		"/key/info":  userInfoQuotaKeyInfoFixture,
		"/user/info": userInfoQuotaUserInfoFixture,
	}}
	repo := &userInfoQuotaRepoStub{}
	svc := NewUpstreamUserInfoQuotaService(repo, nil, upstream, nil)
	return svc, upstream, repo
}

func userInfoQuotaTestAccount() *Account {
	return &Account{
		ID:          6,
		Name:        "SYNTH_USER",
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "sk-test"},
	}
}

// 探测必须补一次 /user/info 并把用户级 24h 并进 3h/12h 之后，且合并结果要落库
// ——列表页读的是 Extra 快照，只改内存结果不落库等于没修。
func TestQueryQuotaForAccount_MergesUserLevel24h(t *testing.T) {
	svc, upstream, repo := newUserInfoQuotaProbeFixture()

	result, err := svc.QueryQuotaForAccount(context.Background(), userInfoQuotaTestAccount())
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Empty(t, result.Error)
	require.Contains(t, upstream.calls, "/user/info", "probe must also hit /user/info")

	require.Len(t, result.Windows, 3)
	w3, w12, w24 := result.Windows[0], result.Windows[1], result.Windows[2]
	require.Equal(t, "3h", w3.Duration)
	require.Equal(t, "12h", w12.Duration)

	require.Equal(t, "24h", w24.Duration)
	require.Equal(t, 100.0, w24.Limit)
	// 用户级窗口必须用 user_info.spend（周期内消耗），不能用 key 的生命周期 spend。
	require.InDelta(t, 0.390654, w24.WindowSpend, 1e-9)
	require.InDelta(t, 99.61, w24.Remaining, 1e-9)
	require.True(t, w24.UsedKnown)
	require.Equal(t, "2026-09-26T16:00:00Z", w24.ResetAt)

	// 约束档仍是最紧的 3h（30 − 0.3907）。
	require.Equal(t, 30.0, result.MaxBudget)
	require.InDelta(t, 29.61, result.Remaining, 1e-9)
	require.Equal(t, "2026-09-26T16:00:00Z", result.BudgetResetAt)
	require.Equal(t, "SYNTH_KEY_ALIAS", result.KeyAlias)

	persisted, ok := repo.updated[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)
	require.True(t, ok, "windows must be persisted to Extra, got %#v", repo.updated[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)])
	require.Len(t, persisted, 3)
	require.Equal(t, "24h", persisted[2].Duration)
}

// /user/info 不可用（403/网络错误）时只丢用户级那一档，key 侧短窗照常返回。
func TestQueryQuotaForAccount_UserInfoFailureKeepsKeyWindows(t *testing.T) {
	for name, status := range map[string]int{
		"forbidden": http.StatusForbidden,
		"not_found": http.StatusNotFound,
		"servererr": http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			svc, _, repo := newUserInfoQuotaProbeFixture()
			svc.httpUpstream = &userInfoQuotaUpstreamStub{
				bodies: map[string]string{"/key/info": userInfoQuotaKeyInfoFixture},
				status: map[string]int{"/user/info": status},
			}

			result, err := svc.QueryQuotaForAccount(context.Background(), userInfoQuotaTestAccount())
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Len(t, result.Windows, 2)
			for _, w := range result.Windows {
				require.NotEqual(t, "24h", w.Duration)
			}
			persisted, ok := repo.updated[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow)
			require.True(t, ok)
			require.Len(t, persisted, 2)
		})
	}
}
