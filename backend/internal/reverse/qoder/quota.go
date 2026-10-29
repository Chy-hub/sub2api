package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"net/http"
)

type QuotaBucket struct {
	Used, Total, Remaining float64
	ResetTime              string
}
type Quota struct {
	Plan                  string
	UserQuota, AddonQuota *QuotaBucket
	IsQuotaExceeded       bool
	ExpiresAt             int64
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
	quota := &Quota{Plan: fetchPlanName(ctx, reverse.HTTPClient(acc.ProxyURL), ep.plan, token), UserQuota: extractQuotaBucket(raw, "userQuota"), AddonQuota: extractQuotaBucket(raw, "addOnQuota"), IsQuotaExceeded: raw["isQuotaExceeded"] == true, ExpiresAt: int64(toFloat(raw, "expiresAt"))}
	result := BuildQuotaResponse(quota)
	return &result, nil
}
func extractQuotaBucket(data map[string]any, key string) *QuotaBucket {
	obj, okObj := data[key].(map[string]any)
	if !okObj || len(obj) == 0 {
		return nil
	}
	bucket := &QuotaBucket{
		Used:      toFloat(obj, "used"),
		Total:     toFloat(obj, "total"),
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
	if quota.IsQuotaExceeded {
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "quota_exceeded", Label: "额度状态", Value: 1, Unit: "已用尽"})
	}
	if quota.ExpiresAt > 0 {
		response.Summary = append(response.Summary,
			reverse.QuotaMetric{Key: "expires_at", Label: "额度到期时间", Value: float64(quota.ExpiresAt), Format: "number", Unit: "unix"})
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
