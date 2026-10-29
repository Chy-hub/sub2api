package workbuddy

// Paths ported from CLIProxyAPI-freetier2api-plugin (MIT).
// 计费域路径。
const (
	billingMeterPath       = "/billing/meter/get-user-resource"
	billingMeterPathV2     = "/v2/billing/meter/get-user-resource"
	legacyDailyCheckinPath = "/billing/meter/daily-checkin"
	// 企业用量接口：个人资源接口对企业账号返回空 Accounts，企业额度只能从这里取。
	enterpriseUsagePath   = "/billing/meter/get-enterprise-user-usage"
	enterpriseUsagePathV2 = "/v2/billing/meter/get-enterprise-user-usage"
	claimGiftPath         = "/billing/meter/claim-gift"
	claimCompPath         = "/billing/meter/claim-compensation"
	trialPath             = "/billing/ide/trial"
)

// 成长 / 任务域路径（挂在 ChatBase 之下）。
const (
	growthTasksPath        = "/v2/activity/growth/tasks"
	growthTasksAcceptPath  = "/v2/activity/growth/tasks/accept"
	growthTasksClaimBase   = "/activity/growth/tasks"
	growthStreakPath       = "/activity/growth/streak"
	growthRedeemPath       = "/activity/growth/redeem"
	growthLotterySummary   = "/activity/growth/lottery/summary"
	growthLotteryDraw      = "/activity/growth/lottery/draw"
	growthMakeupCardsPath  = "/activity/growth/makeup-cards/use"
	buddyInfoPath          = "/activity/growth/buddy/info"
	buddyFirstPath         = "/activity/growth/buddy/first"
	buddyAgreementPath     = "/activity/growth/buddy/agreement"
	buddyTravelStatusPath  = "/activity/growth/buddy/travel/status"
	buddyTravelDepartPath  = "/activity/growth/buddy/travel/depart"
	buddyTravelClaimPath   = "/activity/growth/buddy/travel/claim"
	appearanceSetPath      = "/v2/user-asset/appearance/set"
	expertMarketListPath   = "/portal/operation-platform/market/expert/list"
	schoolBasePath         = "/portal/activity/school"
	globalCountryCodePath  = "/billing/area/get-country-code"
	globalSubmitRegionPath = "/console/login/account"
	globalRegisterPath     = "/auth/realms/copilot/overseas/user/register"
)
