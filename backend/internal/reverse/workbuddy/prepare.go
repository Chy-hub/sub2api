package workbuddy

import (
	"encoding/json"
	"strings"
)

// This file is the "OpenAI body → WorkBuddy body" rewrite pipeline. The step
// order is semantic and must not be reordered; each step fixes a failure mode
// observed on the real upstream:
//
//	 0. model override               — send the bare upstream model name
//	 1. stream forced to true        — the upstream rejects non-stream requests
//	 2. max_completion_tokens alias  — the upstream only honors max_tokens
//	 3. stream_options default       — without it no usage (and no billing) arrives
//	 4. tool_choice normalization    — the upstream field is a string
//	 5. developer → system           — role whitelist excludes developer
//	 6. image_url normalization      — string form is rejected
//	 7. tool-result repack           — interleaved messages break tool pairing
//	 8. orphan tool_call cleanup     — incomplete pairs deadlock the session
//	 9. thinking injection           — deepseek needs an explicit switch
//	10. effort normalization         — illegal efforts are rejected with 400
//	10.5 reasoning_summary default   — mirror the genuine protocol shape
//	11. reasoning_content backfill   — some tenants require non-empty reasoning
//	12. fingerprint sanitization is deliberately NOT ported (see package docs).
//
// Step 12 of the reference implementation removes Claude Code / Codex
// fingerprints so that upstream content moderation does not recognize them. It
// exists to evade upstream detection and is out of scope here.

// prepareOptions is the pipeline input.
type prepareOptions struct {
	sanitize bool
	// model overrides payload.model when non-empty.
	model string
	// promptMode is passthrough (default) / custom / append.
	promptMode string
	// promptText is the custom/appended system prompt.
	promptText string
	// defaultEfforts / supportedEfforts map model → reasoning capability.
	defaultEfforts   map[string]string
	supportedEfforts map[string][]string
	// cacheKey is the prompt_cache_key; empty skips injection.
	cacheKey string
	// global marks a global-realm request (extra system fallback).
	global bool
}

const defaultDeepSeekEffort = "high"

// effortRank orders effort strengths so a request can be downgraded to the
// strongest supported effort that does not exceed it.
var effortRank = map[string]int{
	"off": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6,
}

// prepareBody runs the rewrite pipeline. Unparseable input is returned as-is:
// the upstream's real error is more diagnostic than a local parsing failure.
func prepareBody(body []byte, opts prepareOptions) []byte {
	payload := decodeBodyMap(body)
	if payload == nil {
		return body
	}

	if model := strings.TrimSpace(opts.model); model != "" {
		payload["model"] = model
	}

	// 1) The upstream rejects non-stream requests.
	payload["stream"] = true

	// 2) max_completion_tokens → max_tokens.
	translateMaxCompletionTokens(payload)

	// The internal stream must carry usage even for a non-streaming client or
	// an explicit include_usage=false. The outer gateway owns client formatting
	// and needs these counters for billing.
	options, okOptions := payload["stream_options"].(map[string]any)
	if !okOptions {
		options = map[string]any{}
	}
	options["include_usage"] = true
	payload["stream_options"] = options

	// 4) tool_choice normalization.
	normalizeToolChoice(payload)

	// 5) developer → system.
	normalizeRoles(payload)

	// 5.5) system-prompt handling (passthrough / replace / append).
	applyPromptMode(payload, opts.promptMode, opts.promptText)

	// 6) image_url normalization.
	normalizeImageURL(payload)

	if messages, okMessages := payload["messages"].([]any); okMessages {
		// 7) tool-result repack.
		messages = repackToolResultBlocks(messages)
		// 8) orphan tool_call cleanup.
		messages = cleanupOrphanToolCalls(messages)
		payload["messages"] = messages
	}

	model := strings.TrimSpace(stringField(payload, "model"))

	// 9) thinking injection.
	injectThinking(payload, defaultEffortFor(opts.defaultEfforts, model))

	// 10) effort normalization.
	normalizeReasoningEffort(payload, opts.supportedEfforts[model])

	// 10.5) reasoning_summary.
	injectReasoningSummary(payload)

	// 11) reasoning_content backfill.
	backfillReasoningContent(payload)

	// Global paths require a leading system message.
	if opts.global {
		ensureConsoleSystem(payload)
	}

	// prompt_cache_key: injected last because no earlier step depends on it.
	if cacheKey := strings.TrimSpace(opts.cacheKey); cacheKey != "" {
		if _, okKey := payload["prompt_cache_key"]; !okKey {
			payload["prompt_cache_key"] = cacheKey
		}
	}

	// Run last: prompt handling and tool normalization can introduce text.
	if opts.sanitize {
		if messages, ok := payload["messages"].([]any); ok {
			sanitizeMessages(messages)
		}
	}
	encoded, errEncode := json.Marshal(payload)
	if errEncode != nil {
		return body
	}
	return encoded
}

// translateMaxCompletionTokens maps the alias to max_tokens. The alias is
// always deleted; an explicit max_tokens wins; only positive integers are
// translated (0/null mean "unset", negatives are invalid).
func translateMaxCompletionTokens(payload map[string]any) {
	alias, hasAlias := payload["max_completion_tokens"]
	if !hasAlias {
		return
	}
	delete(payload, "max_completion_tokens")

	if _, hasExplicit := payload["max_tokens"]; hasExplicit {
		return
	}
	if value, okValue := positiveInt64(alias); okValue {
		payload["max_tokens"] = value
	}
}

func positiveInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 || typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return typed, true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return int64(typed), true
	}
	return 0, false
}

// normalizeToolChoice folds tool_choice into the string form the upstream
// accepts; "none" drops the selector and the tool definitions.
func normalizeToolChoice(payload map[string]any) {
	raw, hasChoice := payload["tool_choice"]
	if !hasChoice {
		return
	}
	if text, okText := raw.(string); okText {
		if strings.EqualFold(strings.TrimSpace(text), "none") {
			delete(payload, "tool_choice")
			delete(payload, "tools")
			delete(payload, "functions")
		}
		return
	}
	object, okObject := raw.(map[string]any)
	if !okObject {
		delete(payload, "tool_choice")
		return
	}
	choiceType := strings.ToLower(stringField(object, "type"))
	switch choiceType {
	case "none":
		delete(payload, "tool_choice")
		delete(payload, "tools")
		delete(payload, "functions")
	case "auto", "required":
		payload["tool_choice"] = choiceType
	case "function":
		name := ""
		if function := nestedObject(object, "function"); function != nil {
			name = stringField(function, "name")
		}
		if name == "" {
			name = stringField(object, "name")
		}
		if name != "" {
			payload["tool_choice"] = name
			return
		}
		delete(payload, "tool_choice")
	default:
		delete(payload, "tool_choice")
	}
}

// normalizeRoles rewrites developer to system; every other role is untouched.
func normalizeRoles(payload map[string]any) {
	messages, okMessages := payload["messages"].([]any)
	if !okMessages {
		return
	}
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		if strings.EqualFold(stringField(message, "role"), "developer") {
			message["role"] = "system"
		}
	}
}

// applyPromptMode handles the per-account system prompt:
// custom replaces all system/developer messages, append inserts after the
// leading system blocks.
func applyPromptMode(payload map[string]any, mode, text string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	text = strings.TrimSpace(text)
	if mode == "" || mode == "passthrough" || text == "" {
		return
	}
	messages, okMessages := payload["messages"].([]any)
	if !okMessages {
		return
	}
	switch mode {
	case "custom":
		kept := make([]any, 0, len(messages)+1)
		kept = append(kept, map[string]any{"role": "system", "content": text})
		for _, item := range messages {
			message, okMessage := item.(map[string]any)
			if !okMessage {
				kept = append(kept, item)
				continue
			}
			switch strings.ToLower(stringField(message, "role")) {
			case "system", "developer":
				continue
			}
			kept = append(kept, item)
		}
		payload["messages"] = kept
	case "append":
		insertAt := 0
		for insertAt < len(messages) {
			message, okMessage := messages[insertAt].(map[string]any)
			if !okMessage {
				break
			}
			switch strings.ToLower(stringField(message, "role")) {
			case "system", "developer":
				insertAt++
				continue
			}
			break
		}
		out := make([]any, 0, len(messages)+1)
		out = append(out, messages[:insertAt]...)
		out = append(out, map[string]any{"role": "system", "content": text})
		out = append(out, messages[insertAt:]...)
		payload["messages"] = out
	}
}

// normalizeImageURL converts string image_url values to the object form. The
// upstream only accepts objects.
func normalizeImageURL(payload map[string]any) {
	messages, okMessages := payload["messages"].([]any)
	if !okMessages {
		return
	}
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		parts, okParts := message["content"].([]any)
		if !okParts {
			continue
		}
		for _, part := range parts {
			partMap, okPartMap := part.(map[string]any)
			if !okPartMap || stringField(partMap, "type") != "image_url" {
				continue
			}
			if url, okURL := partMap["image_url"].(string); okURL {
				partMap["image_url"] = map[string]any{"url": url}
			}
		}
	}
}

// ensureConsoleSystem prepends a fallback system message on global requests,
// whose paths expect messages[0] to be a system message.
func ensureConsoleSystem(payload map[string]any) {
	messages, okMessages := payload["messages"].([]any)
	if !okMessages || len(messages) == 0 {
		return
	}
	if first, okFirst := messages[0].(map[string]any); okFirst {
		if strings.EqualFold(stringField(first, "role"), "system") {
			return
		}
	}
	payload["messages"] = append([]any{map[string]any{
		"role":    "system",
		"content": "You are a helpful assistant.",
	}}, messages...)
}

func defaultEffortFor(table map[string]string, model string) string {
	if len(table) == 0 {
		return ""
	}
	return strings.TrimSpace(table[strings.TrimSpace(model)])
}

func isDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// injectThinking enables thinking for deepseek models, which do not return
// reasoning unless the switch and an effort are both present.
func injectThinking(payload map[string]any, defaultEffort string) {
	if !isDeepSeekModel(stringField(payload, "model")) {
		return
	}
	thinking, okThinking := payload["thinking"].(map[string]any)
	if !okThinking {
		payload["thinking"] = map[string]any{"type": "enabled"}
		ensureDeepSeekEffort(payload, defaultEffort)
		return
	}
	switch strings.ToLower(stringField(thinking, "type")) {
	case "disabled":
		// Explicitly disabled: respect it and drop the contradictory effort.
		delete(payload, "reasoning_effort")
		delete(payload, "reasoningEffort")
	case "enabled":
		ensureDeepSeekEffort(payload, defaultEffort)
	default:
		thinking["type"] = "enabled"
		ensureDeepSeekEffort(payload, defaultEffort)
	}
}

func ensureDeepSeekEffort(payload map[string]any, defaultEffort string) {
	if _, okSnake := payload["reasoning_effort"]; okSnake {
		return
	}
	if _, okCamel := payload["reasoningEffort"]; okCamel {
		return
	}
	effort := strings.TrimSpace(defaultEffort)
	if effort == "" {
		effort = defaultDeepSeekEffort
	}
	payload["reasoning_effort"] = effort
}

// normalizeReasoningEffort downgrades an unsupported effort to the nearest
// supported one. An empty support table passes everything through.
func normalizeReasoningEffort(payload map[string]any, supported []string) {
	if len(supported) == 0 {
		return
	}
	for _, key := range []string{"reasoning_effort", "reasoningEffort"} {
		raw, okRaw := payload[key].(string)
		if !okRaw {
			continue
		}
		requested := strings.ToLower(strings.TrimSpace(raw))
		if requested == "" || containsString(supported, requested) {
			continue
		}
		payload[key] = nearestEffort(supported, effortRank[requested])
	}
}

func nearestEffort(supported []string, requestedRank int) string {
	lowest := ""
	lowestRank := 99
	nearest := ""
	nearestRank := -1
	for _, item := range supported {
		rank, okRank := effortRank[item]
		if !okRank {
			continue
		}
		if rank < lowestRank {
			lowestRank = rank
			lowest = item
		}
		if rank <= requestedRank && rank > nearestRank {
			nearestRank = rank
			nearest = item
		}
	}
	if nearest != "" {
		return nearest
	}
	if lowest != "" {
		return lowest
	}
	return supported[0]
}

// injectReasoningSummary adds "auto" when thinking is on and the caller did
// not specify a summary policy.
func injectReasoningSummary(payload map[string]any) {
	if _, okSummary := payload["reasoning_summary"]; okSummary {
		return
	}
	if !reasoningGateOpen(payload) {
		return
	}
	payload["reasoning_summary"] = "auto"
}

// backfillReasoningContent fills reasoning fields on assistant messages when
// thinking is enabled or the history already carries reasoning traces.
func backfillReasoningContent(payload map[string]any) {
	if !reasoningGateOpen(payload) {
		return
	}
	messages, okMessages := payload["messages"].([]any)
	if !okMessages {
		return
	}
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage || !strings.EqualFold(stringField(message, "role"), "assistant") {
			continue
		}
		if _, okString := message["reasoning_content"].(string); !okString {
			if reasoning, okReasoning := message["reasoning"].(string); okReasoning {
				message["reasoning_content"] = reasoning
			} else {
				message["reasoning_content"] = ""
			}
		}
		// Mirror field: keep non-empty values, otherwise use a single space
		// (some tenants validate len(reasoning) > 0, where whitespace passes).
		reasoning, _ := message["reasoning"].(string)
		if strings.TrimSpace(reasoning) == "" {
			if filled, okFilled := message["reasoning_content"].(string); okFilled && strings.TrimSpace(filled) != "" {
				message["reasoning"] = filled
			} else {
				message["reasoning"] = " "
			}
		}
	}
}

func reasoningGateOpen(payload map[string]any) bool {
	if thinking, okThinking := payload["thinking"].(map[string]any); okThinking {
		if strings.EqualFold(stringField(thinking, "type"), "enabled") {
			return true
		}
	}
	messages, _ := payload["messages"].([]any)
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		if reasoning, okReasoning := message["reasoning"].(string); okReasoning && strings.TrimSpace(reasoning) != "" {
			return true
		}
		if _, okContent := message["reasoning_content"]; okContent {
			return true
		}
	}
	return false
}

// repackToolResultBlocks moves non-tool messages that were interleaved between
// an assistant tool_calls message and its results to after the whole group.
// Order changes only; content is untouched.
func repackToolResultBlocks(messages []any) []any {
	if len(messages) < 3 {
		return messages
	}
	out := make([]any, 0, len(messages))
	changed := false

	for index := 0; index < len(messages); index++ {
		message, okMessage := messages[index].(map[string]any)
		if !okMessage || !hasToolCalls(message) {
			out = append(out, messages[index])
			continue
		}

		group := []any{messages[index]}
		var displaced []any
		cursor := index + 1
		for ; cursor < len(messages); cursor++ {
			next, okNext := messages[cursor].(map[string]any)
			if !okNext {
				break
			}
			if strings.EqualFold(stringField(next, "role"), "tool") {
				group = append(group, messages[cursor])
				continue
			}
			// Hand the next tool_calls group back to the outer loop.
			if hasToolCalls(next) {
				break
			}
			displaced = append(displaced, messages[cursor])
		}
		if len(displaced) > 0 {
			changed = true
		}
		out = append(out, group...)
		out = append(out, displaced...)
		index = cursor - 1
	}
	if !changed {
		return messages
	}
	return out
}

// cleanupOrphanToolCalls keeps only tool calls that have a matching result (and
// only results whose call was kept). Symmetric trimming avoids the upstream's
// 11148 "tool calls and tool results do not match".
func cleanupOrphanToolCalls(messages []any) []any {
	callIDs := map[string]bool{}
	resultIDs := map[string]bool{}

	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		switch strings.ToLower(stringField(message, "role")) {
		case "assistant":
			for _, id := range toolCallIDs(message) {
				callIDs[id] = true
			}
		case "tool":
			if id := stringField(message, "tool_call_id"); id != "" {
				resultIDs[id] = true
			}
		}
	}
	if len(callIDs) == 0 && len(resultIDs) == 0 {
		return messages
	}

	keep := map[string]bool{}
	for id := range callIDs {
		if resultIDs[id] {
			keep[id] = true
		}
	}

	out := make([]any, 0, len(messages))
	changed := false
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			out = append(out, item)
			continue
		}
		switch strings.ToLower(stringField(message, "role")) {
		case "assistant":
			if !hasToolCalls(message) {
				out = append(out, message)
				continue
			}
			filtered := filterToolCalls(message, keep)
			if len(filtered) == 0 {
				delete(message, "tool_calls")
				changed = true
			} else {
				message["tool_calls"] = filtered
			}
			out = append(out, message)
		case "tool":
			if !keep[stringField(message, "tool_call_id")] {
				changed = true
				continue
			}
			out = append(out, message)
		default:
			out = append(out, message)
		}
	}
	if !changed {
		return messages
	}
	return out
}

func toolCallIDs(message map[string]any) []string {
	calls, okCalls := message["tool_calls"].([]any)
	if !okCalls {
		return nil
	}
	out := make([]string, 0, len(calls))
	for _, item := range calls {
		call, okCall := item.(map[string]any)
		if !okCall {
			continue
		}
		if id := stringField(call, "id"); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func hasToolCalls(message map[string]any) bool {
	calls, okCalls := message["tool_calls"].([]any)
	return okCalls && len(calls) > 0
}

func filterToolCalls(message map[string]any, keep map[string]bool) []any {
	calls, _ := message["tool_calls"].([]any)
	out := make([]any, 0, len(calls))
	for _, item := range calls {
		call, okCall := item.(map[string]any)
		if !okCall || !keep[stringField(call, "id")] {
			continue
		}
		out = append(out, call)
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
