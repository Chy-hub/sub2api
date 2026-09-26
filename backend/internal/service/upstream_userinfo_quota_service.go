package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// 上游 /key/info 额度探测服务（LiteLLM 网关，如 api.llm.ustc.edu.cn）。
//
// GET {base}/key/info + Bearer api_key → 直接返回当前 key 的预算信息
// （max_budget / spend / budget_limits / budget_limits_usage）。短窗口的
// 窗口内用量从 budget_limits_usage[duration].current_spend 直读，无需基线。
// 结果落 account.Extra 快照，供账号列表用量窗口展示余额。

const (
	userInfoQuotaUpstreamTimeout = 15 * time.Second
	userInfoQuotaMaxBodyBytes    = 256 * 1024
)

// UpstreamUserInfoQuotaService 探测已知 LiteLLM 网关的预算额度。
type UpstreamUserInfoQuotaService struct {
	accountRepo  AccountRepository
	proxyRepo    ProxyRepository
	httpUpstream HTTPUpstream
	cfg          *config.Config
	flight       singleflight.Group
}

// NewUpstreamUserInfoQuotaService 构造 /key/info 额度探测服务。
func NewUpstreamUserInfoQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *UpstreamUserInfoQuotaService {
	return &UpstreamUserInfoQuotaService{
		accountRepo:  accountRepo,
		proxyRepo:    proxyRepo,
		httpUpstream: httpUpstream,
		cfg:          cfg,
	}
}

// QueryQuota 探测指定账号的预算额度并落 Extra 快照。
func (s *UpstreamUserInfoQuotaService) QueryQuota(ctx context.Context, accountID int64) (*UserInfoQuotaResult, error) {
	account, err := s.loadAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.QueryQuotaForAccount(ctx, account)
}

// QueryQuotaForAccount 探测已加载账号（避免二次 GetByID）。
func (s *UpstreamUserInfoQuotaService) QueryQuotaForAccount(ctx context.Context, account *Account) (*UserInfoQuotaResult, error) {
	if s == nil || s.accountRepo == nil || s.httpUpstream == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_NOT_CONFIGURED", "upstream userinfo quota service is not configured")
	}
	if err := validateUserInfoQuotaAccount(account); err != nil {
		return nil, err
	}
	key := "userinfo_quota:" + strconv.FormatInt(account.ID, 10)
	resultCh := s.flight.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.Background(), userInfoQuotaUpstreamTimeout+5*time.Second)
		defer cancel()
		return s.queryQuotaForAccount(probeCtx, account)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case flightResult := <-resultCh:
		if flightResult.Err != nil {
			return nil, flightResult.Err
		}
		result, ok := flightResult.Val.(*UserInfoQuotaResult)
		if !ok || result == nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_RESULT_INVALID", "invalid upstream userinfo quota probe result")
		}
		cloned := *result
		return &cloned, nil
	}
}

func (s *UpstreamUserInfoQuotaService) queryQuotaForAccount(ctx context.Context, account *Account) (*UserInfoQuotaResult, error) {
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(account.GetCredential("access_token"))
	}
	if apiKey == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_NO_APIKEY", "account api_key is empty")
	}

	targetURL := userInfoQuotaURL(account.userInfoQuotaBaseURL())
	if targetURL == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_NO_URL", "account base_url is empty")
	}
	// 出站 URL 安全策略（与网关转发/CN 探测同一套校验）。
	validatedURL, err := cnValidateProbeURL(s.cfg, targetURL)
	if err != nil {
		return nil, infraerrors.New(http.StatusForbidden, "USERINFO_QUOTA_URL_REJECTED", err.Error())
	}
	targetURL = validatedURL

	proxyURL := s.resolveProxyURL(ctx, account)
	callCtx, cancel := context.WithTimeout(ctx, userInfoQuotaUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "USERINFO_QUOTA_REQUEST_BUILD_FAILED", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "USERINFO_QUOTA_REQUEST_FAILED", "upstream request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, userInfoQuotaMaxBodyBytes))

	now := time.Now().UTC()
	result := &UserInfoQuotaResult{
		Provider:   "upstream_userinfo",
		FetchedAt:  now.Unix(),
		StatusCode: resp.StatusCode,
		Unit:       "CNY",
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		result.Error = fmt.Sprintf("Authentication failed (HTTP %d)", resp.StatusCode)
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("API error (HTTP %d): %s", resp.StatusCode, truncate(strings.TrimSpace(string(bodyBytes)), 240))
		return result, nil
	}

	// LiteLLM /key/info 形如 {"key": "<hash>", "info": {...}}，预算字段在 info 下；
	// 兼容顶层直接给预算字段的变体。形状无法识别时按错误处理，
	// 不能落成「不限额」快照覆盖上一次真实数据。
	root := gjson.ParseBytes(bodyBytes)
	keyNode := userInfoKeyNode(root)
	if !keyNode.IsObject() {
		result.Error = "unrecognized /key/info response shape"
		return result, nil
	}

	spend := parseUserInfoF64(keyNode.Get("spend"))

	// 多层预算：顶层 max_budget + budget_limits[] 全部窗口都要展示。
	// 主窗口（budget_duration 与 spend 同口径）直接用 spend；
	// 短窗口从 budget_limits_usage[duration].current_spend 直读。
	primaryDuration := strings.TrimSpace(keyNode.Get("budget_duration").String())
	maxBudget, remaining, resetAt, budgetWindows := userInfoBudgetWindows(keyNode, spend, primaryDuration)

	blocked := keyNode.Get("blocked").Bool()
	expiresRaw := strings.TrimSpace(keyNode.Get("expires").String())
	if expiresRaw == "" {
		expiresRaw = strings.TrimSpace(keyNode.Get("expires_at").String())
	}
	// 无到期时间：未封禁即可用（预算型 key 可能永久有效）；
	// 到期时间无法解析时同样只看 blocked，避免误杀。
	valid := !blocked
	if expiresRaw != "" {
		if t, err := parseUserInfoTime(expiresRaw); err == nil {
			valid = !blocked && t.After(now)
			result.ExpiresAt = t.UTC().Format(time.RFC3339)
		}
	}

	result.Success = true
	result.MaxBudget = maxBudget
	result.Spend = spend
	result.Remaining = remaining
	result.Valid = valid
	result.Blocked = blocked
	result.BudgetResetAt = resetAt
	result.KeyAlias = strings.TrimSpace(keyNode.Get("key_alias").String())
	result.Windows = budgetWindows

	// 全部键无条件写入：UpdateExtra 用 JSONB || 合并，不在 updates 里的键
	// 会永久残留，上游不再返回 expires/reset_at/windows 时必须用空值覆盖掉旧快照。
	updates := map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget):   maxBudget,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):    spend,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixRemain):   remaining,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixValid):    valid,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated):  now.Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixExpires):  result.ExpiresAt,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixResetAt):  result.BudgetResetAt,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixKeyAlias): result.KeyAlias,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows):  budgetWindows,
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("userinfo_quota_persist_failed", "account_id", account.ID, "error", err)
	} else {
		result.Persisted = true
	}
	return result, nil
}

// userInfoKeyNode 从 /key/info 响应里取出预算字段所在节点。
// /key/info 形如 {"key": "<hash>", "info": {...}}；兼容顶层直接给字段的变体。
// 节点必须能识别出预算/身份字段，否则返回空 Result（响应形状异常，
// 不能落成「不限额」快照）。
func userInfoKeyNode(root gjson.Result) gjson.Result {
	if !root.IsObject() {
		return gjson.Result{}
	}
	node := root.Get("info")
	if !node.IsObject() {
		// info 为 null / 缺失时回退顶层（gjson 对 null 的 Exists() 为 true，
		// 必须用 IsObject 判定）。
		node = root
	}
	if node.Get("spend").Exists() || node.Get("max_budget").Exists() ||
		node.Get("key_alias").Exists() || node.Get("budget_duration").Exists() ||
		node.Get("budget_limits").Exists() {
		return node
	}
	return gjson.Result{}
}

// parseUserInfoF64 解析 JSON 数值或字符串为 float64（兼容 "100" 与 100）。
func parseUserInfoF64(node gjson.Result) float64 {
	if !node.Exists() {
		return 0
	}
	switch node.Type {
	case gjson.Number:
		return node.Num
	case gjson.String:
		s := strings.TrimSpace(node.Str)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// parseUserInfoTime 解析 LiteLLM 的时间字段。
// 兼容 RFC3339 / RFC3339Nano、带毫秒与 +08:00 偏移的写法
// （如 "2053-11-04T13:46:10.113+08:00"），以及 Unix 时间戳（秒或毫秒）。
func parseUserInfoTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
		// 大于 1e12 按毫秒解析，否则按秒。
		if f > 1e12 {
			return time.UnixMilli(int64(f)).UTC(), nil
		}
		return time.Unix(int64(f), 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", raw)
}

// userInfoBudgetWindows 收集全部预算窗口，按窗口内用量算剩余。
//
// 主窗口（顶层 max_budget + budget_duration，与 spend 同口径）直接用 spend；
// budget_limits[] 的短窗口从 budget_limits_usage[duration].current_spend 直读
// —— LiteLLM 已对外暴露各窗口独立计数器。上游未提供时 UsedKnown=false。
//
// remaining / maxBudget 取「剩余最少」的窗口（任一档触顶即不可用）。
func userInfoBudgetWindows(keyNode gjson.Result, spend float64, primaryDuration string) (maxBudget, remaining float64, resetAt string, windows []UserInfoBudgetWindow) {
	type rawWindow struct {
		duration string
		limit    float64
		resetAt  string
		primary  bool
	}
	var raws []rawWindow

	add := func(limitNode, resetNode, durationNode gjson.Result, primary bool) {
		if !limitNode.Exists() || limitNode.Type == gjson.Null {
			return
		}
		limit := parseUserInfoF64(limitNode)
		if limit <= 0 {
			return
		}
		w := rawWindow{limit: limit, primary: primary}
		if durationNode.Exists() && durationNode.Type != gjson.Null {
			w.duration = strings.TrimSpace(durationNode.String())
		}
		if resetNode.Exists() && resetNode.Type != gjson.Null {
			if t, err := parseUserInfoTime(resetNode.String()); err == nil {
				w.resetAt = t.UTC().Format(time.RFC3339)
			}
		}
		raws = append(raws, w)
	}

	add(keyNode.Get("max_budget"), keyNode.Get("budget_reset_at"), keyNode.Get("budget_duration"), true)
	if limits := keyNode.Get("budget_limits"); limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			add(item.Get("max_budget"), item.Get("reset_at"), item.Get("budget_duration"), false)
			return true
		})
	}

	if len(raws) == 0 {
		return 0, 0, "", nil
	}

	// 按 limit 升序展示（3h → 12h → 24h），最紧的在前。
	// SliceStable：同 limit 的窗口保持相对顺序，避免前端按 index 取色抖动。
	sort.SliceStable(raws, func(i, j int) bool { return raws[i].limit < raws[j].limit })

	usage := keyNode.Get("budget_limits_usage")
	windows = make([]UserInfoBudgetWindow, 0, len(raws))
	for _, r := range raws {
		windowSpend, known := userInfoWindowSpend(r.primary, r.duration, spend, primaryDuration, usage)
		remain := 0.0
		usedPercent := 0.0
		if known {
			remain = math.Round((r.limit-windowSpend)*100) / 100
			if remain < 0 {
				remain = 0
			}
			usedPercent = math.Round(windowSpend/r.limit*1000) / 10
		}
		windows = append(windows, UserInfoBudgetWindow{
			Duration:    r.duration,
			Limit:       r.limit,
			Remaining:   remain,
			UsedPercent: usedPercent,
			WindowSpend: windowSpend,
			UsedKnown:   known,
			ResetAt:     r.resetAt,
		})
	}

	// 「剩余最少」的已知窗口作为约束档；都未知时退回第一档的 limit。
	best := raws[0]
	bestRemain := math.Inf(1)
	for i, r := range raws {
		w := windows[i]
		if !w.UsedKnown {
			continue
		}
		if rem := r.limit - w.WindowSpend; rem < bestRemain {
			best, bestRemain = r, rem
		}
	}
	if math.IsInf(bestRemain, 1) {
		// 没有任何已知窗口（罕见）：拿第一档 limit 兜底，剩余=limit。
		return best.limit, best.limit, best.resetAt, windows
	}
	remaining = math.Round(bestRemain*100) / 100
	if remaining < 0 {
		remaining = 0
	}
	return best.limit, remaining, best.resetAt, windows
}

// userInfoWindowSpend 计算单窗口的窗口内消耗。
//
// 主窗口与 spend 同口径，直接返回 spend；
// 短窗口从 budget_limits_usage[duration].current_spend 直读（LiteLLM 已暴露
// 各窗口独立计数器）。上游未提供该字段时 known=false，不给百分比。
func userInfoWindowSpend(primary bool, duration string, spend float64, primaryDuration string, usage gjson.Result) (windowSpend float64, known bool) {
	if primary || duration == "" || duration == primaryDuration {
		return spend, true
	}
	u := usage.Get(duration)
	if !u.IsObject() && u.Type != gjson.Number && u.Type != gjson.String {
		return 0, false
	}
	// 形如 {"current_spend": 2.01}；兼容直接给数值的变体。
	if node := u.Get("current_spend"); node.Exists() {
		ws := parseUserInfoF64(node)
		if ws < 0 {
			ws = 0
		}
		return ws, true
	}
	if u.Type == gjson.Number || u.Type == gjson.String {
		ws := parseUserInfoF64(u)
		if ws < 0 {
			ws = 0
		}
		return ws, true
	}
	return 0, false
}

func (s *UpstreamUserInfoQuotaService) loadAccount(ctx context.Context, accountID int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusNotFound, "USERINFO_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", err)
	}
	if err := validateUserInfoQuotaAccount(account); err != nil {
		return nil, err
	}
	return account, nil
}

func validateUserInfoQuotaAccount(account *Account) error {
	if account == nil {
		return infraerrors.New(http.StatusNotFound, "USERINFO_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	if !account.SupportsUserInfoQuota() {
		return infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_UNSUPPORTED", "account is not a recognized /key/info quota upstream")
	}
	return nil
}

func (s *UpstreamUserInfoQuotaService) resolveProxyURL(ctx context.Context, account *Account) string {
	if account == nil || account.ProxyID == nil {
		return ""
	}
	if account.Proxy != nil {
		return account.Proxy.URL()
	}
	if s != nil && s.proxyRepo != nil {
		if proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && proxy != nil {
			account.Proxy = proxy
			return proxy.URL()
		}
	}
	return ""
}
