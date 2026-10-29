package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var (
	ErrReverseGatewayUnavailable    = infraerrors.ServiceUnavailable("REVERSE_GATEWAY_UNAVAILABLE", "reverse gateway is not configured")
	ErrReverseLoginTicketInvalid    = infraerrors.BadRequest("REVERSE_LOGIN_TICKET_INVALID", "reverse login ticket is invalid or expired")
	ErrReverseLoginPlatformMismatch = infraerrors.BadRequest("REVERSE_LOGIN_PLATFORM_MISMATCH", "reverse login ticket platform does not match the account platform")
)

// 反代账号的 extra JSONB 键。运行态字段由服务写入，编辑账号时必须保留；
// auto_checkin / prompt 配置由表单编辑，不做保护。
const (
	ReverseAutoCheckinExtraKey  = "auto_checkin"
	ReverseCheckinExtraKey      = "reverse_checkin"
	ReversePromptModeExtraKey   = "reverse_prompt_mode"
	ReversePromptTextExtraKey   = "reverse_prompt_text"
	ReverseModelCatalogExtraKey = "reverse_model_catalog"
)

// 反代 prompt_mode 取值（与 WorkBuddy 插件一致）。
const (
	ReversePromptModePassthrough = "passthrough"
	ReversePromptModeCustom      = "custom"
	ReversePromptModeAppend      = "append"
)

// IsReversePlatform 报告平台是否由内置反代适配层处理。
func IsReversePlatform(platform string) bool {
	return domain.IsReversePlatform(platform)
}

// IsReverseProvider 报告平台是否属于指定反代适配器（qoder / workbuddy）。
func IsReverseProvider(platform, provider string) bool {
	got, _, ok := domain.ReverseProviderOf(platform)
	return ok && got == strings.TrimSpace(provider)
}
