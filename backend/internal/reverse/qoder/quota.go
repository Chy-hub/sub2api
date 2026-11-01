package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

type QuotaBucket struct {
	Used, Total, Remaining float64
	ResetTime              string
}
type Quota struct {
	Plan                          string
	UserQuota, AddonQuota, Shared *QuotaBucket
	IsQuotaExceeded               bool
	ExpiresAt                     int64
}

func (p *Provider) Quota(ctx context.Context, acc *reverse.Account) (*reverse.QuotaFetchResponse, error) {
	br, err := p.bridgeFor(ctx, acc)
	if err != nil {
		return nil, err
	}
	token := br.session.Identity.SecurityOauthToken
	ep := endpointsFor(br.region)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.quota, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := reverse.HTTPClient(acc.ProxyURL).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readBodyLimited(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, upstreamError(resp.StatusCode, resp.Header, body, token)
	}
	raw, err := decodeJSONResponse(body)
	if err != nil {
		return nil, err
	}
	quota := &Quota{Plan: fetchPlanName(ctx, reverse.HTTPClient(acc.ProxyURL), ep.plan, token), UserQuota: extractQuotaBucket(raw, "userQuota"), AddonQuota: extractQuotaBucket(raw, "addOnQuota"), Shared: extractSharedPackage(raw), IsQuotaExceeded: raw["isQuotaExceeded"] == true, ExpiresAt: parseQuotaExpiresAt(raw["expiresAt"])}
	result := BuildQuotaResponse(quota)
	return &result, nil
}
func extractQuotaBucket(data map[string]any, key string) *QuotaBucket {
	obj, okObj := data[key].(map[string]any)
	if !okObj {
		return nil
	}
	return bucketFromObject(obj)
}

// extractSharedPackage 读取「共享资源包」。
//
// 团队账号的主额度不在 userQuota（订阅席位）里：席位用尽后（3000/3000），
// 真正还能用的额度在顶层 orgResourcePackage 中。该桶的总额字段叫 cap 而不是
// total，另带 available 说明当前能否取用；available=false（过期 / 停用 / 已
// 退还）时余额取不出来，按「没有这份额度」处理，否则页面会展示一份无法使用
// 的数字。
func extractSharedPackage(data map[string]any) *QuotaBucket {
	obj, okObj := data["orgResourcePackage"].(map[string]any)
	if !okObj {
		return nil
	}
	if available, okAvailable := obj["available"].(bool); okAvailable && !available {
		return nil
	}
	return bucketFromObject(obj)
}

// bucketFromObject 把一个额度桶对象读成 QuotaBucket；空对象视为「没有这份额度」。
//
// 总额接受 total 与 cap 两种拼写：组织共享资源包用 cap，其余桶用 total。
func bucketFromObject(obj map[string]any) *QuotaBucket {
	if len(obj) == 0 {
		return nil
	}
	total := toFloat(obj, "total")
	if _, hasTotal := obj["total"]; !hasTotal {
		total = toFloat(obj, "cap")
	}
	bucket := &QuotaBucket{
		Used:      toFloat(obj, "used"),
		Total:     total,
		Remaining: toFloat(obj, "remaining"),
	}
	if reset, okReset := obj["resetTime"].(string); okReset {
		bucket.ResetTime = reset
	} else if reset, okReset := obj["reset_time"].(string); okReset {
		bucket.ResetTime = reset
	}
	return bucket
}

// BuildQuotaResponse 把上游额度翻译成宿主的归一化结构。
func BuildQuotaResponse(quota *Quota) reverse.QuotaFetchResponse {
	response := reverse.QuotaFetchResponse{}
	if quota.Plan != "" {
		response.Subscription = &reverse.QuotaSubscription{
			Plan: quota.Plan, TierName: quota.Plan, TierID: quota.Plan,
		}
	}
	if group := quotaGroup("套餐额度", quota.UserQuota); group != nil {
		response.Groups = append(response.Groups, *group)
	}
	if group := quotaGroup("个人拓展包", quota.AddonQuota); group != nil {
		response.Groups = append(response.Groups, *group)
	}
	if group := quotaGroup("共享资源包", quota.Shared); group != nil {
		response.Groups = append(response.Groups, *group)
	}
	if quota.UserQuota != nil {
		// total 必须一并输出：页面的「剩余/总额 + 使用率」要它才能算比例。
		// 上游对 Free 计划可能给 total=0（额度按剩余量计），此时仍输出 0——
		// 页面会显示 "—"，比编一个数诚实。
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "user_quota_remaining", Label: "套餐剩余额度", Value: quota.UserQuota.Remaining, Unit: "credits"},
			reverse.QuotaMetric{Key: "user_quota_total", Label: "套餐总额度", Value: quota.UserQuota.Total, Unit: "credits"},
			reverse.QuotaMetric{Key: "user_quota_used", Label: "套餐已用额度", Value: quota.UserQuota.Used, Unit: "credits"})
	}
	if quota.AddonQuota != nil {
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "addon_quota_remaining", Label: "拓展包剩余额度", Value: quota.AddonQuota.Remaining, Unit: "credits"},
			reverse.QuotaMetric{Key: "addon_quota_total", Label: "拓展包总额度", Value: quota.AddonQuota.Total, Unit: "credits"},
			reverse.QuotaMetric{Key: "addon_quota_used", Label: "拓展包已用额度", Value: quota.AddonQuota.Used, Unit: "credits"})
	}
	if quota.Shared != nil {
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "shared_quota_remaining", Label: "共享资源包剩余额度", Value: quota.Shared.Remaining, Unit: "credits"},
			reverse.QuotaMetric{Key: "shared_quota_total", Label: "共享资源包总额度", Value: quota.Shared.Total, Unit: "credits"},
			reverse.QuotaMetric{Key: "shared_quota_used", Label: "共享资源包已用额度", Value: quota.Shared.Used, Unit: "credits"})
	}
	if quota.IsQuotaExceeded {
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "quota_exceeded", Label: "额度状态", Value: 1, Unit: "已用尽"})
	}
	if quota.ExpiresAt > 0 {
		expiresAt := quota.ExpiresAt
		// The quota endpoint may return milliseconds; the normalized "unix"
		// metric is seconds, matching the other account-management timestamps.
		if expiresAt >= 1e12 {
			expiresAt /= 1000
		}
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "expires_at", Label: "额度到期时间", Value: float64(expiresAt), Format: "number", Unit: "unix"})
	}
	return response
}

// quotaGroup 把额度桶转成「剩余比例」分组。
func quotaGroup(displayName string, bucket *QuotaBucket) *reverse.QuotaGroup {
	if bucket == nil {
		return nil
	}
	fraction := 0.0
	switch {
	case bucket.Total > 0:
		fraction = reverse.ClampFraction(bucket.Remaining / bucket.Total)
	case bucket.Remaining > 0:
		fraction = 1
	}
	description := fmt.Sprintf("已用 %.0f / 总计 %.0f，剩余 %.0f", bucket.Used, bucket.Total, bucket.Remaining)
	return &reverse.QuotaGroup{
		DisplayName: displayName,
		Buckets: []reverse.QuotaBucket{{
			Window:            "billing-cycle",
			RemainingFraction: fraction,
			ResetTime:         bucket.ResetTime,
			Description:       description,
		}},
	}
}

// parseQuotaExpiresAt accepts epoch seconds, numeric strings and RFC3339
// timestamps; otherwise a string-valued field silently became 0 and the expiry
// was omitted from the UI.
func parseQuotaExpiresAt(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		text := strings.TrimSpace(typed)
		if parsed, err := strconv.ParseInt(text, 10, 64); err == nil {
			return parsed
		}
		if at, err := time.Parse(time.RFC3339, text); err == nil {
			return at.Unix()
		}
	}
	return 0
}

// toFloat 把 JSON 里的数值字段读成 float64（兼容多种数字形态）。
func toFloat(m map[string]any, key string) float64 {
	switch value := m[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		parsed, _ := value.Float64()
		return parsed
	}
	return 0
}

// 签到状态取值。
