package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

const (
	checkinClaimed        = "claimed"
	checkinAlreadyClaimed = "already_claimed"
	checkinNoCampaign     = "no_campaign"
	checkinWindowNotOpen  = "window_not_open"
	checkinWindowMissed   = "window_missed"
	checkinNotConfirmed   = "not_confirmed"
	checkinBlocked        = "blocked"
	checkinNoToken        = "no_token"
	checkinError          = "error"
)

// campaignZone is the fixed UTC+8 offset used by Qoder's daily campaign
// windows (China has had no DST since 1991, so a fixed zone matches
// Asia/Shanghai and needs no tzdata in the container image).
var campaignZone = time.FixedZone("UTC+8", 8*60*60)

// dailyRoundHour is when a new claim window opens (10:00 UTC+8); each window
// runs until 10:00 the next day and missed windows are not made up.
const dailyRoundHour = 10

type checkinIdentity struct {
	Token   string
	Type    string
	Code    string
	Version string
}

func (i checkinIdentity) native() bool { return i.Token != "" && i.Type != "" }

// checkinIdentityFromCredentials only accepts user-supplied machine identity.
// Deriving one is pointless: the upstream filters every campaign when the
// machine token/type pair was not issued by the official desktop client.
func checkinIdentityFromCredentials(acc *reverse.Account) checkinIdentity {
	identity := checkinIdentity{
		Token:   acc.CredentialString("machine_token"),
		Type:    acc.CredentialString("machine_type"),
		Code:    acc.CredentialString("machine_code"),
		Version: firstNonEmpty(acc.CredentialString("desktop_version"), acc.CredentialString("qoder_desktop_version"), desktopVersion),
	}
	if !identity.native() {
		return checkinIdentity{Version: identity.Version}
	}
	return identity
}

type campaignBenefit struct {
	Kind   string `json:"kind"`
	Amount int    `json:"amount"`
}

type campaign struct {
	CampaignID             string           `json:"campaignId"`
	CampaignKey            string           `json:"campaignKey"`
	ActionType             string           `json:"actionType"`
	ClaimStatus            string           `json:"claimStatus"`
	StartAt                int64            `json:"startAt"`
	EndAt                  int64            `json:"endAt"`
	Benefit                *campaignBenefit `json:"benefit"`
	RequiredAchievementKey string           `json:"requiredAchievementKey"`
	AchievementCompleted   bool             `json:"achievementCompleted"`
	UnavailableReason      string           `json:"unavailableReason"`
}

type claimResult struct {
	GrantID     string           `json:"grantId"`
	Status      string           `json:"status"`
	Replayed    bool             `json:"replayed"`
	Benefit     *campaignBenefit `json:"benefit"`
	FailureCode string           `json:"failureCode"`
	ErrorCode   string           `json:"errorCode"`
	ErrorMsg    string           `json:"errorMessage"`
}

type rewardResult struct {
	Status    string           `json:"status"`
	GrantedAt string           `json:"grantedAt"`
	Benefit   *campaignBenefit `json:"benefit"`
}

type dailyStatus struct {
	Status             string `json:"status"`
	CurrentStreakDays  int    `json:"currentStreakDays"`
	TotalClaimDays     int    `json:"totalClaimDays"`
	TotalRewardCredits int    `json:"totalRewardCredits"`
}

// Checkin runs the daily campaign check-in for an account.
func (p *Provider) Checkin(ctx context.Context, acc *reverse.Account) (*reverse.CheckinResult, error) {
	if acc == nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "missing_account", "qoder account is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	bearer := deviceTokenFromSecret(accountAccessToken(acc))
	if bearer == "" {
		return &reverse.CheckinResult{
			Success: false,
			Message: "qoder 凭证里没有可用 token",
			Details: map[string]any{"status": checkinNoToken, "not_recorded": true},
		}, nil
	}
	r := normalizeRegion(acc.Region)
	identity := checkinIdentityFromCredentials(acc)
	client := reverse.HTTPClient(acc.ProxyURL)

	result := campaignsCheckin(ctx, client, r, bearer, identity)
	if status, _ := result.Details["http_status"].(int); status == http.StatusUnauthorized {
		return result, reverse.NewUpstreamError(status, "credential_invalid", result.Message)
	}
	if ctx.Err() != nil && !result.Success {
		return result, ctx.Err()
	}
	readDailyStats(ctx, client, r, bearer, identity, result)
	return result, nil
}

// deviceTokenFromSecret accepts a plain device token or the qoder2api export
// shape ({"device_token":"dt-...","refresh_token":"drt-..."}).
func deviceTokenFromSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || strings.HasPrefix(secret, "dt-") || strings.HasPrefix(secret, "drt-") {
		return secret
	}
	var payload struct {
		DeviceToken string `json:"device_token"`
	}
	if json.Unmarshal([]byte(secret), &payload) == nil && payload.DeviceToken != "" {
		return strings.TrimSpace(payload.DeviceToken)
	}
	return secret
}

func campaignsCheckin(ctx context.Context, client *http.Client, r region, bearer string, identity checkinIdentity) *reverse.CheckinResult {
	status, body, raw := doSashRequest(ctx, client, r, http.MethodGet, "/sash/api/v1/me/campaigns", bearer, identity, nil)
	if status != http.StatusOK {
		result := checkinFailure(checkinError, fmt.Sprintf("查询活动失败 HTTP %d: %s", status, redactSecrets(truncateRunes(raw, 300), bearer, identity.Token)))
		result.Details["http_status"] = status
		return result
	}
	list, ok := body.(map[string]any)
	if !ok {
		return checkinFailure(checkinError, fmt.Sprintf("活动列表格式异常: %s", redactSecrets(truncateRunes(raw, 300), bearer, identity.Token)))
	}
	campaigns := parseCampaigns(list["campaigns"])
	now := time.Now().In(campaignZone)

	var current, past *campaign
	for index := range campaigns {
		item := &campaigns[index]
		if item.ActionType != "CLAIM_BENEFIT" {
			continue
		}
		if item.StartAt <= 0 {
			if current == nil {
				current = item
			}
			continue
		}
		if campaignWindowCovers(item, now) {
			if current == nil || item.ClaimStatus == "CLAIMABLE" {
				current = item
			}
			continue
		}
		if past == nil {
			past = item
		}
	}

	if current != nil {
		windowDate := campaignWindowDate(current)
		switch current.ClaimStatus {
		case "CLAIMABLE":
			if !isTodayRoundWindow(current, now) {
				return windowNotOpenResult(windowDate)
			}
			return claimCampaign(ctx, client, r, bearer, identity, current, windowDate)
		case "CLAIMED":
			if !isTodayRoundWindow(current, now) {
				return windowNotOpenResult(windowDate)
			}
			return &reverse.CheckinResult{Success: true, Message: "今日已领取", Date: windowDate, Details: map[string]any{"status": checkinAlreadyClaimed, "campaign_id": current.CampaignID, "window_date": windowDate}}
		case "ACHIEVEMENT_NOT_COMPLETED":
			message := "需先完成指定成就才能领取"
			if current.RequiredAchievementKey != "" {
				message = fmt.Sprintf("需先完成成就 %s 才能领取", current.RequiredAchievementKey)
			}
			return checkinFailureWithDate(checkinNoCampaign, message, windowDate)
		default:
			suffix := ""
			if current.UnavailableReason != "" {
				suffix = "：" + current.UnavailableReason
			}
			return checkinFailureWithDate(checkinNoCampaign, fmt.Sprintf("活动状态 %s，未领取%s", current.ClaimStatus, suffix), windowDate)
		}
	}

	if past != nil {
		windowDate := campaignWindowDate(past)
		if past.ClaimStatus == "CLAIMED" {
			return windowNotOpenResult(windowDate)
		}
		return checkinFailureWithDate(checkinWindowMissed, fmt.Sprintf("上一轮领取窗口（%s）已结束且未领取，官方不补领", windowDate), windowDate)
	}
	return checkinFailure(checkinNoCampaign, noCampaignMessage(r, identity))
}

func windowNotOpenResult(windowDate string) *reverse.CheckinResult {
	message := fmt.Sprintf("上游领取窗口 %02d:00（UTC+8）开启，当前未开启", dailyRoundHour)
	return checkinFailureWithDate(checkinWindowNotOpen, message, windowDate)
}

func checkinFailure(status, message string) *reverse.CheckinResult {
	return &reverse.CheckinResult{Success: false, Message: message, Details: map[string]any{"status": status, "not_recorded": true}}
}

func checkinFailureWithDate(status, message, windowDate string) *reverse.CheckinResult {
	result := checkinFailure(status, message)
	result.Date = windowDate
	result.Details["window_date"] = windowDate
	return result
}

func noCampaignMessage(r region, identity checkinIdentity) string {
	if r == regionCN {
		return "无可用签到活动"
	}
	if !identity.native() {
		return "国际版活动仅对登录过官方客户端的设备下发：请在账号凭证中填写官方组件输出的 machine_token 与 machine_type（可选 machine_code）后重试"
	}
	return "无可用签到活动（当前设备身份下上游未下发可领取活动）"
}

func claimCampaign(ctx context.Context, client *http.Client, r region, bearer string, identity checkinIdentity, target *campaign, windowDate string) *reverse.CheckinResult {
	quotaBefore, quotaErr := readAddonQuotaTotal(ctx, client, r, bearer)
	hadQuotaBefore := quotaErr == nil

	path := fmt.Sprintf("/sash/api/v1/me/campaigns/%s/claim", target.CampaignID)
	status, body, raw := doSashRequest(ctx, client, r, http.MethodPost, path, bearer, identity, nil)
	if status != http.StatusOK {
		result := checkinFailureWithDate(checkinError, fmt.Sprintf("领取失败 HTTP %d: %s", status, redactSecrets(truncateRunes(raw, 300), bearer, identity.Token)), windowDate)
		result.Details["http_status"] = status
		return result
	}
	var claim claimResult
	encoded, _ := json.Marshal(body)
	if err := json.Unmarshal(encoded, &claim); err != nil {
		return checkinFailureWithDate(checkinError, fmt.Sprintf("领取响应格式异常: %s", redactSecrets(truncateRunes(raw, 300), bearer, identity.Token)), windowDate)
	}
	switch strings.ToUpper(claim.Status) {
	case "CLAIMED":
		if claim.Replayed {
			return &reverse.CheckinResult{Success: true, Message: "今日已领取", Date: windowDate, Details: map[string]any{"status": checkinAlreadyClaimed, "campaign_id": target.CampaignID, "window_date": windowDate}}
		}
		return confirmClaim(ctx, client, r, bearer, identity, target, claim, quotaBefore, hadQuotaBefore, windowDate)
	case "BLOCKED":
		return checkinFailureWithDate(checkinBlocked, "上游以风控去重为由拒绝发放"+claimFailureSuffix(claim), windowDate)
	case "ALREADY_CLAIMED", "REPLAYED":
		return &reverse.CheckinResult{Success: true, Message: "今日已领取", Date: windowDate, Details: map[string]any{"status": checkinAlreadyClaimed, "campaign_id": target.CampaignID, "window_date": windowDate}}
	case "":
		return checkinFailureWithDate(checkinError, fmt.Sprintf("领取响应缺少状态: %s", redactSecrets(truncateRunes(raw, 300), bearer, identity.Token)), windowDate)
	default:
		return checkinFailureWithDate(checkinNoCampaign, fmt.Sprintf("上游状态 %s，未领取%s", claim.Status, claimFailureSuffix(claim)), windowDate)
	}
}

func confirmClaim(ctx context.Context, client *http.Client, r region, bearer string, identity checkinIdentity, target *campaign, claim claimResult, quotaBefore float64, hadQuotaBefore bool, windowDate string) *reverse.CheckinResult {
	amount := 0
	if claim.Benefit != nil {
		amount = claim.Benefit.Amount
	}
	granted, rewardAmount := readCampaignReward(ctx, client, r, bearer, identity, target.CampaignID)
	if rewardAmount > 0 {
		amount = rewardAmount
	}
	quotaGrew := false
	if quotaAfter, err := readAddonQuotaTotal(ctx, client, r, bearer); err == nil && hadQuotaBefore {
		quotaGrew = quotaAfter > quotaBefore
	}
	if granted || quotaGrew {
		message := fmt.Sprintf("领取成功 +%d (%s)", amount, target.CampaignKey)
		if amount == 0 {
			message = fmt.Sprintf("领取成功（%s，上游未给出积分数）", target.CampaignKey)
		}
		reward := ""
		if amount > 0 {
			reward = fmt.Sprintf("+%d", amount)
		}
		return &reverse.CheckinResult{
			Success: true, Message: message, Reward: reward, Date: windowDate,
			Details: map[string]any{"status": checkinClaimed, "amount": amount, "campaign_id": target.CampaignID, "window_date": windowDate},
		}
	}
	result := checkinFailureWithDate(checkinNotConfirmed, fmt.Sprintf("上游未确认发放（%s），请稍后核对额度", target.CampaignKey), windowDate)
	result.Details["campaign_id"] = target.CampaignID
	return result
}

func claimFailureSuffix(claim claimResult) string {
	switch {
	case claim.FailureCode != "":
		return "：" + claim.FailureCode
	case claim.ErrorCode != "":
		return "：" + claim.ErrorCode
	case claim.ErrorMsg != "":
		return "：" + claim.ErrorMsg
	}
	return ""
}

func readCampaignReward(ctx context.Context, client *http.Client, r region, bearer string, identity checkinIdentity, campaignID string) (bool, int) {
	path := fmt.Sprintf("/sash/api/v1/me/campaigns/%s/reward", campaignID)
	status, body, _ := doSashRequest(ctx, client, r, http.MethodGet, path, bearer, identity, nil)
	if status != http.StatusOK {
		return false, 0
	}
	var reward rewardResult
	encoded, _ := json.Marshal(body)
	if json.Unmarshal(encoded, &reward) != nil {
		return false, 0
	}
	if !strings.EqualFold(reward.Status, "CLAIMED") && reward.GrantedAt == "" {
		return false, 0
	}
	amount := 0
	if reward.Benefit != nil {
		amount = reward.Benefit.Amount
	}
	return true, amount
}

func readDailyStats(ctx context.Context, client *http.Client, r region, bearer string, identity checkinIdentity, result *reverse.CheckinResult) {
	status, body, _ := doSashRequest(ctx, client, r, http.MethodGet, "/sash/api/v1/me/daily-check-in/status", bearer, identity, nil)
	if status != http.StatusOK {
		return
	}
	var parsed dailyStatus
	encoded, _ := json.Marshal(body)
	if json.Unmarshal(encoded, &parsed) != nil || parsed.Status == "" || strings.EqualFold(parsed.Status, "DISABLED") {
		return
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	result.Details["streak_days"] = parsed.CurrentStreakDays
	result.Details["total_claim_days"] = parsed.TotalClaimDays
	result.Details["total_reward_credits"] = parsed.TotalRewardCredits
}

func parseCampaigns(raw any) []campaign {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]campaign, 0, len(items))
	for _, item := range items {
		encoded, err := json.Marshal(item)
		if err != nil {
			continue
		}
		var parsed campaign
		if json.Unmarshal(encoded, &parsed) == nil {
			out = append(out, parsed)
		}
	}
	return out
}

func campaignWindowCovers(item *campaign, now time.Time) bool {
	if item.StartAt <= 0 {
		return true
	}
	start := time.Unix(item.StartAt, 0).In(campaignZone)
	if now.Before(start) {
		return false
	}
	if item.EndAt > 0 {
		if end := time.Unix(item.EndAt, 0).In(campaignZone); !now.Before(end) {
			return false
		}
	}
	return true
}

// isTodayRoundWindow reports whether the campaign belongs to the round opened
// at today's 10:00 UTC+8. Before 10:00 the upstream still serves yesterday's
// round (its window covers "now"), which must not be reported as today's.
func isTodayRoundWindow(item *campaign, now time.Time) bool {
	if item.StartAt <= 0 {
		return true
	}
	local := now.In(campaignZone)
	todayOpen := time.Date(local.Year(), local.Month(), local.Day(), dailyRoundHour, 0, 0, 0, campaignZone)
	return !time.Unix(item.StartAt, 0).In(campaignZone).Before(todayOpen)
}

func campaignWindowDate(item *campaign) string {
	if item.StartAt <= 0 {
		return ""
	}
	return time.Unix(item.StartAt, 0).In(campaignZone).Format("2006-01-02")
}

func checkinHeaders(bearer string, identity checkinIdentity) map[string]string {
	headers := map[string]string{
		"authorization":   "Bearer " + bearer,
		"accept":          "application/json",
		"accept-language": "zh-CN",
		"user-agent":      "Qoder",
		"cosy-clienttype": "10",
		"cosy-version":    identity.Version,
	}
	if !identity.native() {
		return headers
	}
	headers["cosy-machinetoken"] = identity.Token
	headers["cosy-machinetype"] = identity.Type
	if identity.Code != "" {
		headers["cosy-machinecode"] = identity.Code
	}
	return headers
}

func doSashRequest(ctx context.Context, client *http.Client, r region, method, path, bearer string, identity checkinIdentity, payload any) (int, any, string) {
	base := endpointsFor(r).sashBase
	var reader *bytes.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, "encode request: " + err.Error()
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return 0, nil, "build request: " + err.Error()
	}
	for key, value := range checkinHeaders(bearer, identity) {
		request.Header.Set(key, value)
	}
	if method == http.MethodPost {
		request.Header.Set("origin", base)
		request.ContentLength = 0
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, "do request: " + err.Error()
	}
	defer func() { _ = response.Body.Close() }()
	data, err := readBodyLimited(response.Body)
	if err != nil {
		return response.StatusCode, nil, "read response: " + err.Error()
	}
	var parsed any
	if len(data) > 0 {
		_ = json.Unmarshal(data, &parsed)
	}
	return response.StatusCode, parsed, string(data)
}

// readAddonQuotaTotal reads the add-on quota total as the pre-claim baseline.
func readAddonQuotaTotal(ctx context.Context, client *http.Client, r region, bearer string) (float64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointsFor(r).quota, nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readBodyLimited(response.Body)
	if err != nil {
		return 0, err
	}
	if response.StatusCode != http.StatusOK {
		return 0, upstreamError(response.StatusCode, response.Header, body, bearer)
	}
	parsed, err := decodeJSONResponse(body)
	if err != nil {
		return 0, err
	}
	bucket, ok := parsed["addOnQuota"].(map[string]any)
	if !ok {
		return 0, nil
	}
	return floatValue(bucket["total"]), nil
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	}
	return 0
}
