package service

import (
	"net/url"
	"strings"
)

// 上游 /key/info 额度探测的主机白名单与账号识别。
//
// 背景：部分 OpenAI 兼容中转（LiteLLM 网关，如 api.llm.ustc.edu.cn）在
// GET /key/info 返回当前 key 的预算额度（max_budget / spend / expires /
// blocked / budget_limits / budget_limits_usage）。sub2api 按 base_url 主机
// 自动识别这类上游，用量窗口展示余额。
//
// 只认名单内主机：探测会把账号 API key 发往 {base}/key/info，
// 不对任意 base_url 盲发。

// userInfoQuotaHosts 支持 /key/info 额度端点的上游主机（小写、无端口）。
var userInfoQuotaHosts = []string{
	"api.llm.ustc.edu.cn",
}

// UserInfoQuotaExtraKey 落 account.Extra 的快照键后缀（加前缀 upstream_userinfo_）。
// 统一放在 upstream_userinfo_ 命名空间下，便于调度侧识别为观测型中性键，
// 避免每次探测都触发事务 + outbox + 平台桶重建。
const (
	userInfoExtraPrefix         = "upstream_userinfo_"
	UserInfoExtraSuffixBudget   = "max_budget"
	UserInfoExtraSuffixSpend    = "spend"
	UserInfoExtraSuffixRemain   = "remaining"
	UserInfoExtraSuffixExpires  = "expires_at"
	UserInfoExtraSuffixValid    = "valid"
	UserInfoExtraSuffixUpdated  = "updated_at"
	UserInfoExtraSuffixResetAt  = "budget_reset_at"
	UserInfoExtraSuffixKeyAlias = "key_alias"
	UserInfoExtraSuffixWindows  = "windows"
)

// UserInfoQuotaExtraKey 拼接 Extra 快照键。
func UserInfoQuotaExtraKey(suffix string) string { return userInfoExtraPrefix + suffix }

// IsUserInfoQuotaUpstream 报告 base_url 主机是否为已知的 /key/info 额度上游。
func IsUserInfoQuotaUpstream(baseURL string) bool {
	host := userInfoQuotaHost(baseURL)
	if host == "" {
		return false
	}
	for _, h := range userInfoQuotaHosts {
		if host == h {
			return true
		}
	}
	return false
}

// SupportsUserInfoQuota 报告账号是否可走 /key/info 额度探测。
// 仅 API Key / Upstream 账号（凭证里有 api_key 与 base_url）。
func (a *Account) SupportsUserInfoQuota() bool {
	if a == nil {
		return false
	}
	if a.Type != AccountTypeAPIKey && a.Type != AccountTypeUpstream {
		return false
	}
	return IsUserInfoQuotaUpstream(a.userInfoQuotaBaseURL())
}

// userInfoQuotaBaseURL 返回用于额度探测的 base_url。
// 优先凭证 base_url（与前端识别一致），回退平台解析的 OpenAI base。
func (a *Account) userInfoQuotaBaseURL() string {
	if a == nil {
		return ""
	}
	if baseURL := strings.TrimSpace(a.GetCredential("base_url")); baseURL != "" {
		return baseURL
	}
	return a.GetOpenAIBaseURL()
}

// userInfoQuotaHost 提取 base_url 的主机名（小写、去端口、去尾点）。
func userInfoQuotaHost(baseURL string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
}

// userInfoQuotaURL 由账号 base_url 派生 /key/info 端点。
// 兼容 base_url 带 /v1 前缀的写法（如 https://host/v1 → https://host/key/info）。
func userInfoQuotaURL(baseURL string) string {
	return userInfoSiteURL(baseURL, "/key/info")
}

// userInfoUserURL 由账号 base_url 派生 /user/info 端点。
// LiteLLM 的用户级预算（max_budget + budget_duration，如 24h）挂在 user 上，
// key 的 /key/info 只有短窗 budget_limits，24h 必须从 /user/info 补。
func userInfoUserURL(baseURL string) string {
	return userInfoSiteURL(baseURL, "/user/info")
}

func userInfoSiteURL(baseURL, suffix string) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return ""
	}
	if !strings.Contains(baseURL, "://") {
		baseURL = "https://" + baseURL
	}
	// 去掉尾部斜杠后，剥离末级 /v1（LiteLLM 管理端点在站点根）。
	trimmed := strings.TrimRight(baseURL, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return trimmed + suffix
}

// UserInfoBudgetWindow 单档预算窗口（如 3h/12h/24h 多层预算）。
type UserInfoBudgetWindow struct {
	// Duration 窗口周期（如 "3h" / "12h" / "24h"），来自 budget_duration。
	Duration string `json:"duration,omitempty"`
	// Limit 该窗口的预算上限。
	Limit float64 `json:"limit"`
	// Remaining 该窗口的剩余额度（limit - window_spend）。
	Remaining float64 `json:"remaining"`
	// UsedPercent 窗口内已用百分比（0-100）。
	UsedPercent float64 `json:"used_percent"`
	// WindowSpend 窗口内消耗（主窗口 = spend；短窗口 = budget_limits_usage 直读）。
	WindowSpend float64 `json:"window_spend"`
	// UsedKnown false = 上游未提供窗口内消耗，百分比不可信，前端降级为限额+倒计时。
	// 主窗口（与 spend 同口径）恒为 true。
	UsedKnown bool `json:"used_known"`
	// ResetAt 该窗口的重置时间（RFC3339）。
	ResetAt string `json:"reset_at,omitempty"`
}

// UserInfoQuotaResult 是 /key/info 额度探测结果（管理端 + 前端消费）。
type UserInfoQuotaResult struct {
	Provider  string  `json:"provider"`
	Success   bool    `json:"success"`
	Remaining float64 `json:"remaining"`
	MaxBudget float64 `json:"max_budget"`
	Spend     float64 `json:"spend"`
	Unit      string  `json:"unit"`
	Valid     bool    `json:"valid"`
	ExpiresAt string  `json:"expires_at,omitempty"`
	Blocked   bool    `json:"blocked,omitempty"`
	// BudgetResetAt 当前约束窗口的重置时间（RFC3339），空表示无周期预算。
	BudgetResetAt string `json:"budget_reset_at,omitempty"`
	// KeyAlias 当前 key 的别名，便于核对取的是哪一个。
	KeyAlias string `json:"key_alias,omitempty"`
	// Windows 全部预算窗口（3h/12h/24h 等），多层预算同时展示。
	Windows    []UserInfoBudgetWindow `json:"windows,omitempty"`
	StatusCode int                    `json:"status_code,omitempty"`
	FetchedAt  int64                  `json:"fetched_at"`
	Persisted  bool                   `json:"persisted"`
	Error      string                 `json:"error,omitempty"`
}
