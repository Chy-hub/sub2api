package qoder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// 本文件实现「请求身份链」里两处出站身份：会话标识的连续性，和 User-Agent。
//
// 背景：上游判定请求是否来自官方客户端时，会话字段的连续性比 UA 更难伪造也更
// 难解释。真实客户端在同一个对话里复用 session_id；如果每个请求都换一个新会话，
// 账号下就永远只有「第一轮」，这本身就是一条自动化痕迹。这里的做法是：能从请求体
// 里拿到会话标识就稳定派生 session_id，拿不到才退回每请求随机（保持旧行为）。

// defaultUserAgent 是账号侧出站接口的默认 UA（对话与目录是 cosy 签名，额度走 Bearer）。
//
// 它与官方 IDE 客户端的 UA 并不相同，这是已知的画像差异；上游目前只校验 cosy 签名
// 与机器身份，所以没有改默认值——换成一个猜的 UA 只会把「不一致」变成「错的」。
// 需要对齐时在账号凭据里填 user_agent（见 qoderUserAgent），不必改代码。
const defaultUserAgent = "Go-http-client/2.0"

// qoderUserAgent 返回账号显式配置的 User-Agent，未配置或值不可用时返回空串。
//
// 这里做一次规范化：UA 会随每个账号侧请求发出，含控制字符（尤其 CR/LF）的值会让
// http.Client.Do 直接失败。覆盖值原本只影响对话，现在也影响用户信息与 jobToken 交换，
// 一个填错的 UA 会把账号连 bridge 都建不起来——所以坏值退回默认值，而不是让账号失效。
func qoderUserAgent(acc *reverse.Account) string {
	if acc == nil {
		return ""
	}
	return sanitizeUserAgent(acc.CredentialString("user_agent"))
}

// maxUserAgentLen 是 UA 覆盖值的长度上限，超出视为填错。
const maxUserAgentLen = 512

// sanitizeUserAgent 校验并裁剪 UA 覆盖值；不可用时返回空串。
//
// 含控制字符（CR/LF 等）的值一律判为不可用而不是「洗掉再用」：UA 里出现换行只可能是
// 填错或 header 注入，退回默认值比把两个字符串粘在一起更可预期。
func sanitizeUserAgent(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > maxUserAgentLen {
		return ""
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return trimmed
}

// outboundUserAgent 返回账号侧出站 UA：账号覆盖优先，否则默认值。
//
// 覆盖对所有账号侧请求生效（对话、模型目录、额度、用户信息、jobToken 交换、签到），
// 否则同一个账号在不同接口上报出不同的客户端，那本身就是画像不一致。
// 只有登录流程（login.go，此时还没有账号凭据）保留各自默认值。
func outboundUserAgent(acc *reverse.Account) string {
	if override := qoderUserAgent(acc); override != "" {
		return override
	}
	return defaultUserAgent
}

// setUserAgent 只在 userAgent 非空时写入，避免把空串当成「显式没有 UA」。
//
// 空串表示「这个头交给默认值」：账号侧调用点总是传 outboundUserAgent 的非空结果，
// 只有登录流程（还没有账号凭据）传空串，沿用改动前的无 UA 行为。
func setUserAgent(request *http.Request, userAgent string) {
	if request == nil {
		return
	}
	if value := strings.TrimSpace(userAgent); value != "" {
		request.Header.Set("user-agent", value)
	}
}

// sanitizeFingerprints 报告该账号是否启用出站文本指纹净化。
//
// 与 WorkBuddy 的语义一致：默认开启，账号在 extra 里写 false 才关闭。
// 规则表命中的是 harness 模板句（Claude Code / Codex CLI / DSH），与渠道无关，
// 所以两边共用 internal/reverse/textsanitize 的同一份规则。
func sanitizeFingerprints(acc *reverse.Account) bool {
	return acc != nil && acc.Extra["reverse_sanitize_fingerprints"] != false
}

// conversationKey 从 Chat Completions 请求体里提取会话标识。
//
// 提取顺序与 WorkBuddy 的 conversationKey 一致：显式的 metadata / 顶层字段优先，
// 其次是 prompt_cache_key，最后才用 system + 首条 user 的哈希兜底。
// 全部落空时返回空串，调用方退回每请求随机。
func conversationKey(payload map[string]any) string {
	if metadata, okMetadata := payload["metadata"].(map[string]any); okMetadata {
		for _, key := range []string{"conversation_id", "conversationId"} {
			if value := strings.TrimSpace(stringValue(metadata[key])); value != "" {
				return value
			}
		}
	}
	for _, key := range []string{"conversation_id", "conversationId"} {
		if value := strings.TrimSpace(stringValue(payload[key])); value != "" {
			return value
		}
	}
	if value := strings.TrimSpace(stringValue(payload["prompt_cache_key"])); value != "" {
		return value
	}
	return derivedConversationKey(payload)
}

// derivedConversationKey 用 system + 首条 user 的哈希作为会话标识。
//
// 代价要写明：客户端一旦改写 system prompt（换模型、加工具、改系统提示），
// 派生键就会变，那个对话在上游看起来就是一个新会话。需要稳定分组时，
// 调用方应显式传 metadata.conversation_id。
func derivedConversationKey(payload map[string]any) string {
	messages, okMessages := payload["messages"].([]any)
	if !okMessages || len(messages) == 0 {
		return ""
	}
	systemText, firstUser := "", ""
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		text := strings.TrimSpace(normalizeMessageContent(message))
		if text == "" {
			continue
		}
		switch stringValue(message["role"]) {
		case "system", "developer":
			if systemText == "" {
				systemText = text
			}
		case "user":
			if firstUser == "" {
				firstUser = text
			}
		}
		if systemText != "" && firstUser != "" {
			break
		}
	}
	if systemText == "" && firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(systemText + "\x00" + firstUser))
	return "derived:" + hex.EncodeToString(sum[:12])
}

// accountSessionUUID 把（账号, 会话键）映射成会话 id。
//
// 掺入账号身份的理由：账号池会在对话中途换账号，而调用方给的 conversation_id 不变。
// 如果 session 只由会话键决定，同一个会话就会出现在多个账号下——那正是上游最容易
// 用来识别反代的特征。
//
// uid 可能为空：手工导入的凭据不带 uid，而上游 userinfo/jobToken 也可能不回 id。
// 这种账号退回用账号 ID 兜底，隔离照样成立。拿不到会话键时退回每请求随机。
func accountSessionUUID(accountID int64, uid, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return newUUID()
	}
	owner := strings.TrimSpace(uid)
	if owner == "" {
		owner = "acct:" + strconv.FormatInt(accountID, 10)
	}
	return sessionUUID(owner + "|" + key)
}

// sessionUUID 把会话键确定性地映射成 UUID 形状的字符串。
//
// 用 RFC 4122 v5 形态（版本位 5、变体位 10xx）：上游按 UUID 形状读取这些字段，
// 不是 UUID 的形态会被当成缺失或异常值。
func sessionUUID(key string) string {
	if strings.TrimSpace(key) == "" {
		return newUUID()
	}
	sum := sha256.Sum256([]byte("qoder-session:" + key))
	buf := sum[:16]
	buf[6] = (buf[6] & 0x0f) | 0x50
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
