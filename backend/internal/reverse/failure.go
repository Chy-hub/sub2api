package reverse

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Error classifications ported from CLIProxyAPI-freetier2api-plugin (MIT).
type FailureKind string

const (
	FailureKindNone            FailureKind = "none"
	FailureKindHardCredit      FailureKind = "hard_credit"
	FailureKindSoftRate        FailureKind = "soft_rate"
	FailureKindSessionDead     FailureKind = "session_dead"
	FailureKindNotFound        FailureKind = "not_found"
	FailureKindServer          FailureKind = "server"
	FailureKindContentBlocked  FailureKind = "content_blocked"
	FailureKindChannelRejected FailureKind = "channel_rejected"
	FailureKindBadParams       FailureKind = "bad_params"
	FailureKindAccountFault    FailureKind = "account_fault"
	FailureKindModelBlocked    FailureKind = "model_blocked"
	FailureKindWAFBlock        FailureKind = "waf_block"
	FailureKindPromptTooLong   FailureKind = "prompt_too_long"
	FailureKindImageInvalid    FailureKind = "image_invalid"
	FailureKindClient          FailureKind = "client"

	// 兼容通用供应商的短分类。
	FailureKindCredential FailureKind = "credential"
	FailureKindRateLimit  FailureKind = "rate_limit"
	FailureKindTransient  FailureKind = "transient"
	FailureKindQuota      FailureKind = "quota"
	FailureKindBusy       FailureKind = "busy"
)

// String 返回稳定的类别名（用于日志与错误码）。
func (k FailureKind) String() string {
	if k == "" {
		return "unknown"
	}
	return string(k)
}

// Failure 是带分类的上游错误。
//
// Msg 保持上游原文；Code 是上游业务码的可读形式（可为空）。RetryAfter /
// ResetAt 是上游明示的等待信息，零值表示未给出。
type Failure struct {
	Kind       FailureKind
	Status     int
	Code       string
	Msg        string
	RetryAfter time.Duration
	ResetAt    time.Time

	// RetryableOverride 允许供应商覆盖通用的可重试判定（nil 表示按 FailureKind 推导）。
	RetryableOverride *bool
}

// Failure 实现 error 接口。
func (e *Failure) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("upstream %s [%d/%s]: %s", e.Kind, e.Status, e.Code, e.Msg)
	}
	return fmt.Sprintf("upstream %s [%d]: %s", e.Kind, e.Status, e.Msg)
}

// HTTPStatus 返回映射给宿主的 HTTP 状态码。
//
// 上游状态码通常已经是宿主认得的（401/402/429/5xx），优先透传；只有
// Status==0（网络层失败或 200 包体里的业务错误）才按 FailureKind 推导。
func (e *Failure) HTTPStatus() int {
	if e == nil {
		return http.StatusBadGateway
	}
	if e.Status > 0 {
		return e.Status
	}
	switch e.Kind {
	case FailureKindHardCredit, FailureKindQuota:
		return http.StatusPaymentRequired
	case FailureKindSoftRate, FailureKindRateLimit:
		return http.StatusTooManyRequests
	case FailureKindSessionDead, FailureKindCredential:
		return http.StatusUnauthorized
	case FailureKindNotFound, FailureKindModelBlocked:
		return http.StatusNotFound
	case FailureKindServer, FailureKindTransient:
		return http.StatusBadGateway
	case FailureKindWAFBlock:
		return http.StatusForbidden
	case FailureKindBusy:
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

// Retryable 报告宿主是否应该换一个凭证重试同一请求。
func (e *Failure) Retryable() bool {
	if e == nil {
		return false
	}
	if e.RetryableOverride != nil {
		return *e.RetryableOverride
	}
	switch e.Kind {
	case FailureKindContentBlocked, FailureKindChannelRejected, FailureKindPromptTooLong,
		FailureKindImageInvalid, FailureKindBadParams, FailureKindClient, FailureKindCredential:
		return false
	default:
		return true
	}
}

// UpstreamKind 返回错误分类标识。
func (e *Failure) UpstreamKind() string { return e.Kind.String() }

// UpstreamMessage 返回上游原始正文（实现 core.UpstreamDetail）。
func (e *Failure) UpstreamMessage() string {
	if e == nil {
		return ""
	}
	return e.Msg
}

// Bool 返回 bool 指针（构造 RetryableOverride 用）。
func Bool(value bool) *bool { return &value }

func ContainsAny(value string, markers []string) bool {
	for _, marker := range markers {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}
func Truncate(value string, limit int) string {
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}
