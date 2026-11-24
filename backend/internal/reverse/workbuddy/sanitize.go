package workbuddy

import (
	"github.com/Wei-Shaw/sub2api/internal/reverse/textsanitize"
)

// 净化实现已抽到 internal/reverse/textsanitize。
//
// 规则表命中的是 harness 模板句，与具体渠道无关，因此 WorkBuddy 与 Qoder 共用
// 同一份规则，避免两份副本各自漂移。这里只保留 prepare.go 调用的入口，
// 开关判定仍在 forward.go（reverse_sanitize_fingerprints）。
func sanitizeMessages(messages []any) bool {
	return textsanitize.SanitizeMessages(messages)
}
