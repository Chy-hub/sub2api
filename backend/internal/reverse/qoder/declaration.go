package qoder

import (
	"regexp"
	"strings"
)

// 本文件处理「上游按系统提示词里的 agent 声明判定请求来自第三方 agent」这条风控。
//
// 实测结论（外部反馈，omp / kimi code 等 agent 均如此）：把系统提示词里的 agent
// 声明删掉、或换成通用名字，请求就不再被判成第三方 agent。
//
// 为什么通用净化表不够：textsanitize 的规则来自 WorkBuddy 渠道，改写目标是
// 「WorkBuddy」这个品牌——那对 WorkBuddy 是官方名，对 Qoder 只是另一个第三方
// agent 名，声明结构原样保留，照样命中同一判定。所以 Qoder 需要自己的收尾：
// 把声明句换成不含任何产品名的通用身份。
//
// 作用范围只限 system / developer 消息：用户消息里出现同样的句子是用户自己的
// 内容，不该被改写。

// neutralIdentity 是替换后的通用身份声明。刻意不含 "agent"，避免换成另一种
// agent 声明；也不含任何产品名。
const neutralIdentity = "You are a helpful assistant."

var (
	// agentDeclarationSentence 匹配从行首到第一个句号的声明句，句号之后的指令
	// （"Follow the rules below." 之类）保留。行首允许 markdown 标题标记
	// （"# You are ..."），否则带小标题的 system prompt 会漏掉。
	agentDeclarationSentence = regexp.MustCompile(`(?im)^(\s*(?:#{1,6}\s*)?)you are\b[^\n]{0,200}?\b(?:agent|assistant|cli|tool)\b[^.\n]{0,80}\.`)
	// agentDeclarationLine 兜底：整行没有句号的声明。
	agentDeclarationLine = regexp.MustCompile(`(?im)^(\s*(?:#{1,6}\s*)?)you are\b[^\n]{0,200}?\b(?:agent|assistant|cli|tool)\b[^\n]{0,80}$`)
)

// neutralizeDeclaration 把一段文本里的 agent 声明句换成通用身份。
//
// 幂等：通用身份本身也匹配这两条正则，但替换结果与原文相同，不会继续变化。
func neutralizeDeclaration(text string) string {
	if text == "" || !strings.Contains(strings.ToLower(text), "you are") {
		return text
	}
	if agentDeclarationSentence.MatchString(text) {
		return agentDeclarationSentence.ReplaceAllString(text, "${1}"+neutralIdentity)
	}
	if agentDeclarationLine.MatchString(text) {
		return agentDeclarationLine.ReplaceAllString(text, "${1}"+neutralIdentity)
	}
	return text
}

// neutralizeAgentDeclarations 改写请求体里 system / developer 消息的声明句。
//
// 返回值报告是否发生了改动。
func neutralizeAgentDeclarations(messages []any) bool {
	changed := false
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		switch stringValue(message["role"]) {
		case "system", "developer":
		default:
			continue
		}
		if content, okContent := message["content"].(string); okContent {
			if updated := neutralizeDeclaration(content); updated != content {
				message["content"] = updated
				changed = true
			}
			continue
		}
		parts, okParts := message["content"].([]any)
		if !okParts {
			continue
		}
		for _, item := range parts {
			part, okPart := item.(map[string]any)
			if !okPart {
				continue
			}
			text, okText := part["text"].(string)
			if !okText {
				continue
			}
			if updated := neutralizeDeclaration(text); updated != text {
				part["text"] = updated
				changed = true
			}
		}
	}
	return changed
}
