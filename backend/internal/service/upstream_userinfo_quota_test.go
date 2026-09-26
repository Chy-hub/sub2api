package service

import (
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestIsUserInfoQuotaUpstream(t *testing.T) {
	cases := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.llm.ustc.edu.cn", true},
		{"https://api.llm.ustc.edu.cn/v1", true},
		{"https://api.llm.ustc.edu.cn/", true},
		{"api.llm.ustc.edu.cn", true},
		{"https://API.LLM.USTC.EDU.CN", true},
		{"https://api.llm.ustc.edu.cn:8443", true},
		{"https://api.openai.com", false},
		{"https://relay.example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsUserInfoQuotaUpstream(tc.baseURL); got != tc.want {
			t.Errorf("IsUserInfoQuotaUpstream(%q) = %v, want %v", tc.baseURL, got, tc.want)
		}
	}
}

func TestUserInfoQuotaURL(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"https://api.llm.ustc.edu.cn", "https://api.llm.ustc.edu.cn/key/info"},
		{"https://api.llm.ustc.edu.cn/", "https://api.llm.ustc.edu.cn/key/info"},
		{"https://api.llm.ustc.edu.cn/v1", "https://api.llm.ustc.edu.cn/key/info"},
		{"https://api.llm.ustc.edu.cn/v1/", "https://api.llm.ustc.edu.cn/key/info"},
	}
	for _, tc := range cases {
		if got := userInfoQuotaURL(tc.baseURL); got != tc.want {
			t.Errorf("userInfoQuotaURL(%q) = %q, want %q", tc.baseURL, got, tc.want)
		}
	}
}

func TestSupportsUserInfoQuota(t *testing.T) {
	apikey := &Account{
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "sk-x"},
	}
	if !apikey.SupportsUserInfoQuota() {
		t.Fatal("apikey account with ustc base_url should support userinfo quota")
	}
	oauth := &Account{
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn"},
	}
	if oauth.SupportsUserInfoQuota() {
		t.Fatal("oauth account should not support userinfo quota")
	}
	other := &Account{
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.openai.com", "api_key": "sk-x"},
	}
	if other.SupportsUserInfoQuota() {
		t.Fatal("non-listed host should not support userinfo quota")
	}
}

// 用真实 USTC /key/info 形状验证：主窗口 24h 直读 spend，
// 短窗口从 budget_limits_usage 直读窗口内用量。
func TestUserInfoBudgetWindows_MultiWindow(t *testing.T) {
	keyNode := gjson.Parse(`{
		"spend": 7.9387204,
		"max_budget": 100.0,
		"budget_duration": "24h",
		"budget_reset_at": "2026-09-27T00:00:00+08:00",
		"budget_limits": [
			{"reset_at": "2026-09-26T18:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"},
			{"reset_at": "2026-09-27T00:00:00+08:00", "max_budget": 70.0, "budget_duration": "12h"}
		],
		"budget_limits_usage": {
			"3h": {"current_spend": 2.0089},
			"12h": {"current_spend": 2.0089}
		}
	}`)
	spend := parseUserInfoF64(keyNode.Get("spend"))
	maxBudget, remaining, resetAt, windows := userInfoBudgetWindows(keyNode, spend, "24h")

	// 已知窗口剩余：3h=30-2.0089≈27.99，12h=70-2.0089≈67.99，24h=100-7.94≈92.06 → 最紧 3h。
	if maxBudget != 30 {
		t.Fatalf("maxBudget = %v, want 30", maxBudget)
	}
	if remaining != 27.99 {
		t.Fatalf("remaining = %v, want 27.99", remaining)
	}
	if resetAt != "2026-09-26T10:00:00Z" {
		t.Fatalf("resetAt = %q, want %q", resetAt, "2026-09-26T10:00:00Z")
	}
	if len(windows) != 3 {
		t.Fatalf("windows len = %d, want 3", len(windows))
	}
	// 按 limit 升序：3h(30) → 12h(70) → 24h(100)。
	w3, w12, w24 := windows[0], windows[1], windows[2]
	if w3.Duration != "3h" || w3.WindowSpend != 2.0089 || !w3.UsedKnown {
		t.Fatalf("3h window = %+v, want spend=2.0089 known", w3)
	}
	if w12.Duration != "12h" || w12.WindowSpend != 2.0089 || !w12.UsedKnown {
		t.Fatalf("12h window = %+v, want spend=2.0089 known", w12)
	}
	// 主窗口直读 spend。
	if w24.Duration != "24h" || w24.WindowSpend != 7.9387204 || !w24.UsedKnown {
		t.Fatalf("24h window = %+v, want spend=7.9387204 known", w24)
	}
	if w3.UsedPercent != 6.7 {
		t.Fatalf("3h UsedPercent = %v, want 6.7", w3.UsedPercent)
	}
}

// budget_limits_usage 缺失的短窗口：UsedKnown=false，不给百分比；主窗口仍准确。
func TestUserInfoBudgetWindows_MissingUsage(t *testing.T) {
	keyNode := gjson.Parse(`{
		"spend": 20.5,
		"max_budget": 100.0,
		"budget_duration": "24h",
		"budget_reset_at": "2026-09-26T16:00:00Z",
		"budget_limits": [
			{"reset_at": "2026-09-26T18:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"}
		]
	}`)
	_, remaining, _, windows := userInfoBudgetWindows(keyNode, 20.5, "24h")
	// 3h 未知不参与约束，最紧 = 24h 的 79.5。
	if remaining != 79.5 {
		t.Fatalf("remaining = %v, want 79.5", remaining)
	}
	if windows[0].UsedKnown {
		t.Fatalf("3h window should be unknown without budget_limits_usage, got %+v", windows[0])
	}
	if !windows[1].UsedKnown {
		t.Fatalf("24h window should always be known, got %+v", windows[1])
	}
}

// budget_limits_usage 只覆盖部分短窗口时，缺失的那档降级，已知档正常参与约束。
func TestUserInfoBudgetWindows_PartialUsage(t *testing.T) {
	keyNode := gjson.Parse(`{
		"spend": 20.5,
		"max_budget": 100.0,
		"budget_duration": "24h",
		"budget_reset_at": "2026-09-27T00:00:00+08:00",
		"budget_limits": [
			{"reset_at": "2026-09-26T18:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"},
			{"reset_at": "2026-09-27T00:00:00+08:00", "max_budget": 70.0, "budget_duration": "12h"}
		],
		"budget_limits_usage": {
			"3h": {"current_spend": 28.0}
		}
	}`)
	_, remaining, _, windows := userInfoBudgetWindows(keyNode, 20.5, "24h")
	// 3h 已知：剩余 2；12h 未知跳过；24h 剩余 79.5 → 最紧 3h。
	if remaining != 2 {
		t.Fatalf("remaining = %v, want 2", remaining)
	}
	if !windows[0].UsedKnown || windows[0].WindowSpend != 28 {
		t.Fatalf("3h window = %+v, want spend=28 known", windows[0])
	}
	if windows[1].UsedKnown {
		t.Fatalf("12h window should be unknown, got %+v", windows[1])
	}
}

// budget_limits_usage 直接给数值的变体。
func TestUserInfoBudgetWindows_UsageBareNumber(t *testing.T) {
	keyNode := gjson.Parse(`{
		"spend": 5.0,
		"max_budget": 100.0,
		"budget_duration": "24h",
		"budget_reset_at": "2026-09-27T00:00:00+08:00",
		"budget_limits": [
			{"reset_at": "2026-09-26T18:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"}
		],
		"budget_limits_usage": {"3h": 2.0089}
	}`)
	_, _, _, windows := userInfoBudgetWindows(keyNode, 5.0, "24h")
	if !windows[0].UsedKnown || windows[0].WindowSpend != 2.0089 {
		t.Fatalf("3h window = %+v, want spend=2.0089 known", windows[0])
	}
}

func TestUserInfoBudgetWindows_Unlimited(t *testing.T) {
	keyNode := gjson.Parse(`{"spend": 5543.01, "max_budget": null, "budget_limits": null}`)
	maxBudget, remaining, resetAt, windows := userInfoBudgetWindows(keyNode, 5543.01, "")
	if maxBudget != 0 || remaining != 0 || resetAt != "" || windows != nil {
		t.Fatalf("unlimited window got (%v,%v,%q,%v), want (0,0,\"\",nil)", maxBudget, remaining, resetAt, windows)
	}
}

// /key/info 形如 {"key": "<hash>", "info": {...}}，预算字段在 info 下。
func TestUserInfoKeyNodeFromKeyInfo(t *testing.T) {
	root := gjson.Parse(`{
		"key": "3247797b04f83afe549c5ea298aae48681cc42da0608ee8be762c20d87681b34",
		"info": {
			"key_alias": "SA24038005_64938656173",
			"spend": 7.9387204,
			"max_budget": 100.0,
			"budget_duration": "24h",
			"budget_limits": [
				{"reset_at": "2026-09-26T18:00:00+08:00", "max_budget": 30.0, "budget_duration": "3h"}
			],
			"budget_limits_usage": {"3h": {"current_spend": 2.0089}}
		}
	}`)
	keyNode := userInfoKeyNode(root)
	if !keyNode.IsObject() {
		t.Fatal("expected info object")
	}
	if keyNode.Get("key_alias").String() != "SA24038005_64938656173" {
		t.Fatalf("key_alias = %q", keyNode.Get("key_alias").String())
	}
	spend := parseUserInfoF64(keyNode.Get("spend"))
	_, _, _, windows := userInfoBudgetWindows(keyNode, spend, "24h")
	if len(windows) != 2 {
		t.Fatalf("windows len = %d, want 2", len(windows))
	}
	if windows[0].WindowSpend != 2.0089 {
		t.Fatalf("3h window spend = %v, want 2.0089", windows[0].WindowSpend)
	}
}

// userInfoKeyNode：info 为 null / 缺失时回退顶层；无法识别的形状返回空。
func TestUserInfoKeyNode(t *testing.T) {
	// info 为 null：gjson Exists() 对 null 为 true，必须 IsObject 判定后回退顶层。
	node := userInfoKeyNode(gjson.Parse(`{"key": "hash", "info": null, "spend": 5.0, "max_budget": 100.0, "budget_duration": "24h"}`))
	if !node.IsObject() {
		t.Fatal("null info must fall back to root")
	}
	if parseUserInfoF64(node.Get("spend")) != 5.0 {
		t.Fatalf("fall back to root spend = %v, want 5.0", parseUserInfoF64(node.Get("spend")))
	}

	// info 为对象：取 info，忽略顶层。
	node = userInfoKeyNode(gjson.Parse(`{"key": "hash", "spend": 1, "info": {"spend": 7.5, "max_budget": 100}}`))
	if parseUserInfoF64(node.Get("spend")) != 7.5 {
		t.Fatalf("prefer info spend = %v, want 7.5", parseUserInfoF64(node.Get("spend")))
	}

	// 顶层变体（无 info 包装）。
	node = userInfoKeyNode(gjson.Parse(`{"spend": 3.0, "max_budget": 50.0}`))
	if parseUserInfoF64(node.Get("spend")) != 3.0 {
		t.Fatalf("root variant spend = %v, want 3.0", parseUserInfoF64(node.Get("spend")))
	}

	// 仅带 budget_limits 的理论变体也算可识别。
	node = userInfoKeyNode(gjson.Parse(`{"info": {"budget_limits": [{"max_budget": 30, "budget_duration": "3h"}]}}`))
	if !node.IsObject() {
		t.Fatal("budget_limits-only variant must be recognized")
	}

	// 形状无法识别：空对象 / 无关字段，必须返回空（调用方按错误处理，不能落「不限额」快照）。
	for _, raw := range []string{
		`{"info": null}`,
		`{"key": "hash"}`,
		`{"info": {}}`,
		`{"info": {"foo": 1}}`,
		`[]`,
		`"just a string"`,
	} {
		if userInfoKeyNode(gjson.Parse(raw)).IsObject() {
			t.Fatalf("unrecognized shape must return empty, got object for %s", raw)
		}
	}
}

// budget_limits_usage[duration] 为 null 时视作未知，不给百分比。
func TestUserInfoWindowSpend_UsageNull(t *testing.T) {
	usage := gjson.Parse(`{"3h": null}`)
	_, known := userInfoWindowSpend(false, "3h", 1.0, "24h", usage)
	if known {
		t.Fatal("null usage entry must be unknown")
	}
}

func TestParseUserInfoTime_MilliOffset(t *testing.T) {
	// 真实 USTC 返回的到期时间格式：毫秒 + 时区偏移。
	got, err := parseUserInfoTime("2053-11-04T13:46:10.113+08:00")
	if err != nil {
		t.Fatalf("parseUserInfoTime error: %v", err)
	}
	if got.UTC().Format(time.RFC3339) != "2053-11-04T05:46:10Z" {
		t.Fatalf("parsed = %s", got.UTC().Format(time.RFC3339))
	}
}

func TestParseUserInfoTime_UnixNumeric(t *testing.T) {
	// 秒级时间戳。
	got, err := parseUserInfoTime("1790000000")
	if err != nil {
		t.Fatalf("seconds timestamp error: %v", err)
	}
	if got.Unix() != 1790000000 {
		t.Fatalf("seconds timestamp parsed = %d, want 1790000000", got.Unix())
	}
	// 毫秒级时间戳。
	got, err = parseUserInfoTime("1790000000000")
	if err != nil {
		t.Fatalf("millis timestamp error: %v", err)
	}
	if got.Unix() != 1790000000 {
		t.Fatalf("millis timestamp parsed = %d, want 1790000000", got.Unix())
	}
	// 非时间字符串仍然报错（交给调用方退化为只看 blocked）。
	if _, err := parseUserInfoTime("not-a-time"); err == nil {
		t.Fatal("garbage string must not parse as time")
	}
}
