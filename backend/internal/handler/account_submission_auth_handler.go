package handler

import (
	"context"
	"errors"
	"net/http"
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
//   1. 会话归属绑定当前登录用户，而不是管理员；
//   2. 建出来的账号进入待审状态，不直接可用。
type AccountSubmissionOAuthHandler struct {
	contribution *service.AccountContributionService
	openai       *service.OpenAIOAuthService
	gemini       *service.GeminiOAuthService
	antigravity  *service.AntigravityOAuthService
	grok         *service.GrokOAuthService
	reverse      *service.ReverseGateway
}

// NewAccountSubmissionOAuthHandler 构造用户侧授权提交 handler。
func NewAccountSubmissionOAuthHandler(
	contribution *service.AccountContributionService,
	openai *service.OpenAIOAuthService,
	gemini *service.GeminiOAuthService,
	antigravity *service.AntigravityOAuthService,
	grok *service.GrokOAuthService,
	reverse *service.ReverseGateway,
) *AccountSubmissionOAuthHandler {
	return &AccountSubmissionOAuthHandler{
		contribution: contribution,
		openai:       openai,
		gemini:       gemini,
		antigravity:  antigravity,
		grok:         grok,
		reverse:      reverse,
	}
}

// AuthStartRequest 发起授权。
type AuthStartRequest struct {
	TaskID      int64  `json:"task_id" binding:"required"`
	OAuthType   string `json:"oauth_type"`
	RedirectURI string `json:"redirect_uri"`
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

	ctx := c.Request.Context()
	switch task.Platform {
	case service.PlatformOpenAI:
		// OpenAI 的 state 保存在服务端 session 里，不随授权链接返回。
		result, err := h.openai.GenerateAuthURL(ctx, nil, req.RedirectURI, task.Platform)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL})
	case service.PlatformGemini:
		result, err := h.gemini.GenerateAuthURL(ctx, nil, req.RedirectURI, "", normalizedGeminiOAuthType(req.OAuthType), "")
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
		result, err := h.grok.GenerateAuthURL(ctx, nil, req.RedirectURI)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, gin.H{"mode": "oauth", "session_id": result.SessionID, "auth_url": result.AuthURL, "state": result.State})
	default:
		if service.IsReversePlatform(task.Platform) {
			h.startReverseAuth(c, task, subject.UserID)
			return
		}
		response.BadRequest(c, "this task does not support authorization; submit credentials directly")
	}
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
		Pending:  false,
		Status:   "authorized",
		Nickname: result.Profile.Nickname,
	})
}

// AuthCompleteRequest 完成授权并建立待审提交。
type AuthCompleteRequest struct {
	TaskID      int64  `json:"task_id" binding:"required"`
	SessionID   string `json:"session_id"`
	Code        string `json:"code"`
	State       string `json:"state"`
	RedirectURI string `json:"redirect_uri"`
	OAuthType   string `json:"oauth_type"`
	Ticket      string `json:"ticket"`
	Name        string `json:"name"`
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

	platform, accountType, credentials, err := h.exchangeCredentials(c.Request.Context(), task, &req, subject.UserID)
	if err != nil {
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
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, sub)
}

// exchangeCredentials 按任务平台把授权结果换成账号凭证。
// 复用管理员创建账号时的同一批 service 方法，保证凭证结构完全一致。
func (h *AccountSubmissionOAuthHandler) exchangeCredentials(
	ctx context.Context, task *service.AccountTask, req *AuthCompleteRequest, userID int64,
) (string, string, map[string]any, error) {
	switch task.Platform {
	case service.PlatformOpenAI:
		tokenInfo, err := h.openai.ExchangeCode(ctx, &service.OpenAIExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State, RedirectURI: req.RedirectURI,
		})
		if err != nil {
			return "", "", nil, err
		}
		return task.Platform, service.AccountTypeOAuth, h.openai.BuildAccountCredentials(tokenInfo), nil

	case service.PlatformGemini:
		tokenInfo, err := h.gemini.ExchangeCode(ctx, &service.GeminiExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
			OAuthType: normalizedGeminiOAuthType(req.OAuthType),
		})
		if err != nil {
			return "", "", nil, err
		}
		return task.Platform, service.AccountTypeOAuth, h.gemini.BuildAccountCredentials(tokenInfo), nil

	case service.PlatformAntigravity:
		tokenInfo, err := h.antigravity.ExchangeCode(ctx, &service.AntigravityExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State,
		})
		if err != nil {
			return "", "", nil, err
		}
		return task.Platform, service.AccountTypeOAuth, h.antigravity.BuildAccountCredentials(tokenInfo), nil

	case service.PlatformGrok:
		tokenInfo, err := h.grok.ExchangeCode(ctx, &service.GrokExchangeCodeInput{
			SessionID: req.SessionID, Code: req.Code, State: req.State, RedirectURI: req.RedirectURI,
		})
		if err != nil {
			return "", "", nil, err
		}
		return task.Platform, service.AccountTypeOAuth, h.grok.BuildAccountCredentials(tokenInfo), nil
	}

	if service.IsReversePlatform(task.Platform) {
		if h.reverse == nil {
			return "", "", nil, service.ErrReverseGatewayUnavailable
		}
		if strings.TrimSpace(req.Ticket) == "" {
			return "", "", nil, errors.New("login ticket is required")
		}
		platform, credentials, finish, ok := h.reverse.AcquireLoginTicket(req.Ticket, userID)
		if !ok {
			return "", "", nil, errors.New("login ticket is invalid or expired")
		}
		// 票据被消费后，只有在账号真正建立后才算用掉；交给调用方决定。
		defer finish(true)
		return platform, service.AccountTypeReverse, credentials, nil
	}

	return "", "", nil, errors.New("this task does not support authorization")
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
