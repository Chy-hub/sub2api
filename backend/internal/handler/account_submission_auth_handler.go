package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AccountSubmissionOAuthHandler 提供用户侧的「授权式」账号提交。
//
// 不同平台的账号不是靠填一串密钥得来的：OAuth 平台要用户在浏览器完成授权，
// 反代平台（Qoder / WorkBuddy）要走设备授权登录。这里复用管理员创建账号时
// 使用的那批 service，差别只有两点：
//  1. 会话归属绑定当前登录用户，而不是管理员；
//  2. 建出来的账号进入待审状态，不直接可用。
type AccountSubmissionOAuthHandler struct {
	contribution *service.AccountContributionService
	openai       *service.OpenAIOAuthService
	gemini       *service.GeminiOAuthService
	antigravity  *service.AntigravityOAuthService
	grok         *service.GrokOAuthService
	anthropic    *service.OAuthService
	reverse      *service.ReverseGateway
}

// NewAccountSubmissionOAuthHandler 构造用户侧授权提交 handler。
func NewAccountSubmissionOAuthHandler(
	contribution *service.AccountContributionService,
	openai *service.OpenAIOAuthService,
	gemini *service.GeminiOAuthService,
	antigravity *service.AntigravityOAuthService,
	grok *service.GrokOAuthService,
	anthropic *service.OAuthService,
	reverse *service.ReverseGateway,
) *AccountSubmissionOAuthHandler {
	return &AccountSubmissionOAuthHandler{
		contribution: contribution,
		openai:       openai,
		gemini:       gemini,
		antigravity:  antigravity,
		grok:         grok,
		anthropic:    anthropic,
		reverse:      reverse,
	}
}

// AuthStartRequest 发起授权。
type AuthStartRequest struct {
	TaskID int64 `json:"task_id" binding:"required"`
	// AccountType 是用户选择的账号类型；Anthropic 用它区分 oauth / setup-token。
	AccountType string `json:"account_type"`
	OAuthType   string `json:"oauth_type"`
}

// StartAuth 为任务对应的平台发起授权，返回授权链接。
// POST /api/v1/user/account-submissions/auth/start
func (h *AccountSubmissionOAuthHandler) StartAuth(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req AuthStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	task, err := h.contribution.GetTask(c.Request.Context(), req.TaskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !task.Enabled {
		response.ErrorFrom(c, service.ErrAccountTaskDisabled)
		return
	}
	// 用户选择的账号类型必须属于任务允许的集合，提前拦掉避免白跑一次授权。
	if accountType := strings.TrimSpace(req.AccountType); accountType != "" &&
		!slices.Contains(task.AccountTypes, accountType) {
		response.ErrorFrom(c, service.ErrAccountTaskType)
		return
	}

	// 授权链接必须使用上游 OAuth 客户端注册的回调地址。OpenAI / Grok 的公共
	// client 只登记了本地回环地址，传站点路由会让 redirect_uri 校验直接失败。
	// 管理端同样从不传自定义地址，用户从回调页地址栏复制 code 即可。
	ctx := c.Request.Context()
	switch task.Platform {
	case service.PlatformOpenAI:
		result, err := h.openai.GenerateAuthURL(ctx, nil, "", task.Platform)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		// OpenAI 的 ExchangeCode 要求客户端回传 state；它就在授权链接里。
		response.Success(c, gin.H{
			"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL,
			"state": oauthStateFromURL(result.AuthURL),
		})
	case service.PlatformGemini:
		result, err := h.gemini.GenerateAuthURL(ctx, nil, "", "", normalizedGeminiOAuthType(req.OAuthType), "")
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL, "state": result.State})
	case service.PlatformAntigravity:
		result, err := h.antigravity.GenerateAuthURL(ctx, nil)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL, "state": result.State})
	case service.PlatformGrok:
		result, err := h.grok.GenerateAuthURL(ctx, nil, "")
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL, "state": result.State})
	case service.PlatformAnthropic:
		h.startAnthropicAuth(c, strings.TrimSpace(req.AccountType))
	default:
		if service.IsReversePlatform(task.Platform) {
			h.startReverseAuth(c, task, subject.UserID)
			return
		}
		response.BadRequest(c, "this task does not support authorization; submit credentials directly")
	}
}

// startAnthropicAuth 发起 Claude OAuth 或推理专用 setup-token 授权。
func (h *AccountSubmissionOAuthHandler) startAnthropicAuth(c *gin.Context, accountType string) {
	if h.anthropic == nil {
		response.BadRequest(c, "anthropic oauth is not configured")
		return
	}
	var (
		result *service.GenerateAuthURLResult
		err    error
	)
	if accountType == service.AccountTypeSetupToken {
		result, err = h.anthropic.GenerateSetupTokenURL(c.Request.Context(), nil)
	} else {
		result, err = h.anthropic.GenerateAuthURL(c.Request.Context(), nil)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Anthropic 的 ExchangeCode 从服务端 session 读取 state，不需要客户端回传。
	response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL})
}

// oauthStateFromURL 从授权链接里取出 state；取不到时返回空串。
func oauthStateFromURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return parsed.Query().Get("state")
}

// startReverseAuth 反代平台复用设备授权登录，会话归属于当前用户。
func (h *AccountSubmissionOAuthHandler) startReverseAuth(c *gin.Context, task *service.AccountTask, userID int64) {
	if h.reverse == nil {
		response.ErrorFrom(c, service.ErrReverseGatewayUnavailable)
		return
	}
	start, err := h.reverse.StartLogin(c.Request.Context(), task.Platform, 0, userID, nil)
	if err != nil {
		response.Error(c, http.StatusBadGateway, "Failed to start reverse login: "+err.Error())
		return
	}
	response.Success(c, gin.H{
		"mode":       "reverse",
		"session_id": start.SessionID,
		"auth_url":   start.LoginURL,
		"expires_at": start.ExpiresAt,
	})
}

// ReversePollResult 反代登录轮询结果。
type ReversePollResult struct {
	Pending  bool   `json:"pending"`
	AuthURL  string `json:"auth_url,omitempty"`
	Status   string `json:"status,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	TaskID   int64  `json:"task_id,omitempty"`
	// LoginTicket 是登录完成后的短期一次性票据，提交时凭它换取凭证。
	// 与管理员创建账号的流程一致：票据绑定发起登录的用户。
	LoginTicket string `json:"login_ticket,omitempty"`
}

// PollAuth 轮询授权状态；反代平台完成后返回可继续提交的会话 ID。
// GET /api/v1/user/account-submissions/auth/poll?task_id=&session_id=
func (h *AccountSubmissionOAuthHandler) PollAuth(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if sessionID == "" {
		response.BadRequest(c, "session_id is required")
		return
	}
	if h.reverse == nil {
		response.ErrorFrom(c, service.ErrReverseGatewayUnavailable)
		return
	}
	result, err := h.reverse.PollLogin(c.Request.Context(), sessionID, subject.UserID)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if result.Pending {
		response.Success(c, ReversePollResult{Pending: true})
		return
	}
	response.Success(c, ReversePollResult{
		Pending:     false,
		Status:      "authorized",
		Nickname:    result.Profile.Nickname,
		LoginTicket: result.LoginTicket,
	})
}

// AuthCompleteRequest 完成授权并建立待审提交。
type AuthCompleteRequest struct {
	TaskID    int64  `json:"task_id" binding:"required"`
	SessionID string `json:"session_id"`
	Code      string `json:"code"`
	State     string `json:"state"`
	OAuthType string `json:"oauth_type"`
	// AccountType 是用户选择的账号类型；Anthropic 用它区分 oauth / setup-token。
	AccountType string `json:"account_type"`
	// Ticket 是反代平台登录完成后拿到的短期票据。
	Ticket string `json:"ticket"`
	Name   string `json:"name"`
}

// CompleteAuth 用授权结果建立账号并登记待审提交。
// POST /api/v1/user/account-submissions/auth/complete
func (h *AccountSubmissionOAuthHandler) CompleteAuth(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req AuthCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	task, err := h.contribution.GetTask(c.Request.Context(), req.TaskID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	platform, accountType, credentials, extra, finish, err := h.exchangeCredentials(c.Request.Context(), task, &req, subject.UserID)
	if err != nil {
		if finish != nil {
			finish(false)
		}
		response.ErrorFrom(c, err)
		return
	}

	sub, err := h.contribution.SubmitAuthorized(c.Request.Context(), &service.AuthorizedSubmissionInput{
		TaskID:      task.ID,
		UserID:      subject.UserID,
		Name:        req.Name,
		Platform:    platform,
		Type:        accountType,
		Credentials: credentials,
		Extra:       extra,
	})
	// 登录票据只在账号真正建立后才算用掉；失败时放回去，用户不必重新登录。
	if finish != nil {
		finish(err == nil)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sub)
}

// exchangeCredentials 按任务平台把授权结果换成账号凭证与账号元数据。
// 复用管理员创建账号时的同一批 service 方法，保证凭证结构完全一致。
// 返回的 finish 仅反代平台非空：调用方在提交成功后才消费票据。
func (h *AccountSubmissionOAuthHandler) exchangeCredentials(
	ctx context.Context, task *service.AccountTask, req *AuthCompleteRequest, userID int64,
) (string, string, map[string]any, map[string]any, func(bool), error) {
	switch task.Platform {
	case service.PlatformOpenAI:
		tokenInfo, err := h.openai.ExchangeCode(ctx, &service.OpenAIExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
		})
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		extra := map[string]any{}
		if tokenInfo.Email != "" {
			extra["email"] = tokenInfo.Email
		}
		if tokenInfo.PrivacyMode != "" {
			extra["privacy_mode"] = tokenInfo.PrivacyMode
		}
		return task.Platform, service.AccountTypeOAuth, h.openai.BuildAccountCredentials(tokenInfo), extra, nil, nil

	case service.PlatformGemini:
		tokenInfo, err := h.gemini.ExchangeCode(ctx, &service.GeminiExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
			OAuthType: normalizedGeminiOAuthType(req.OAuthType),
		})
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		// Gemini 的 BuildAccountCredentials 已经把 Extra 合并进凭证，这里无需重复。
		return task.Platform, service.AccountTypeOAuth, h.gemini.BuildAccountCredentials(tokenInfo), nil, nil, nil

	case service.PlatformAntigravity:
		tokenInfo, err := h.antigravity.ExchangeCode(ctx, &service.AntigravityExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
		})
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		return task.Platform, service.AccountTypeOAuth, h.antigravity.BuildAccountCredentials(tokenInfo), nil, nil, nil

	case service.PlatformGrok:
		tokenInfo, err := h.grok.ExchangeCode(ctx, &service.GrokExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
		})
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		extra := map[string]any{}
		if tokenInfo.Email != "" {
			extra["email"] = tokenInfo.Email
		}
		if tokenInfo.SubscriptionTier != "" {
			extra["subscription_tier"] = tokenInfo.SubscriptionTier
		}
		if tokenInfo.EntitlementStatus != "" {
			extra["entitlement_status"] = tokenInfo.EntitlementStatus
		}
		return task.Platform, service.AccountTypeOAuth, h.grok.BuildAccountCredentials(tokenInfo), extra, nil, nil

	case service.PlatformAnthropic:
		if h.anthropic == nil {
			return "", "", nil, nil, nil, errors.New("anthropic oauth is not configured")
		}
		tokenInfo, err := h.anthropic.ExchangeCode(ctx, &service.ExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code,
		})
		if err != nil {
			return "", "", nil, nil, nil, err
		}
		accountType := service.AccountTypeOAuth
		if strings.TrimSpace(req.AccountType) == service.AccountTypeSetupToken {
			accountType = service.AccountTypeSetupToken
		}
		// 网关从 Extra 读取 org/account uuid，与管理端创建流程保持一致。
		extra := map[string]any{}
		if tokenInfo.OrgUUID != "" {
			extra["org_uuid"] = tokenInfo.OrgUUID
		}
		if tokenInfo.AccountUUID != "" {
			extra["account_uuid"] = tokenInfo.AccountUUID
		}
		if tokenInfo.EmailAddress != "" {
			extra["email_address"] = tokenInfo.EmailAddress
		}
		return task.Platform, accountType, anthropicTokenCredentials(tokenInfo), extra, nil, nil
	}

	if service.IsReversePlatform(task.Platform) {
		if h.reverse == nil {
			return "", "", nil, nil, nil, service.ErrReverseGatewayUnavailable
		}
		if strings.TrimSpace(req.Ticket) == "" {
			return "", "", nil, nil, nil, errors.New("login ticket is required")
		}
		platform, credentials, finish, ok := h.reverse.AcquireLoginTicket(req.Ticket, userID)
		if !ok {
			return "", "", nil, nil, nil, errors.New("login ticket is invalid or expired")
		}
		return platform, service.AccountTypeReverse, credentials, nil, finish, nil
	}

	return "", "", nil, nil, nil, errors.New("this task does not support authorization")
}

// anthropicTokenCredentials 把 Claude OAuth 结果转成账号凭证。
// 管理员侧直接展开 tokenInfo；这里按同样的键名构造，空值不写入。
func anthropicTokenCredentials(token *service.TokenInfo) map[string]any {
	if token == nil {
		return map[string]any{}
	}
	credentials := map[string]any{"access_token": token.AccessToken}
	if token.TokenType != "" {
		credentials["token_type"] = token.TokenType
	}
	if token.ExpiresIn != 0 {
		credentials["expires_in"] = token.ExpiresIn
	}
	if token.ExpiresAt != 0 {
		credentials["expires_at"] = token.ExpiresAt
	}
	if token.RefreshToken != "" {
		credentials["refresh_token"] = token.RefreshToken
	}
	if token.Scope != "" {
		credentials["scope"] = token.Scope
	}
	if token.OrgUUID != "" {
		credentials["org_uuid"] = token.OrgUUID
	}
	if token.AccountUUID != "" {
		credentials["account_uuid"] = token.AccountUUID
	}
	if token.EmailAddress != "" {
		credentials["email_address"] = token.EmailAddress
	}
	return credentials
}

// normalizedGeminiOAuthType 与管理员侧保持一致：默认 code_assist。
func normalizedGeminiOAuthType(raw string) string {
	value := strings.TrimSpace(raw)
	switch value {
	case "code_assist", "google_one", "ai_studio":
		return value
	default:
		return "code_assist"
	}
}
