package workbuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The upstream always answers with OpenAI-shaped SSE (`data: {...}` frames and
// a trailing `data: [DONE]`). Passing it through verbatim is unsafe: upstream
// frames occasionally carry the whole message in `message` instead of `delta`,
// repeat function.name on every tool_call fragment, or omit id/model. This file
// therefore rebuilds frames from a whitelist and re-emits them as standard
// chat.completion.chunk events.

// errEmptyStream reports an upstream stream without any data event.
var errEmptyStream = errors.New("workbuddy: upstream stream contained no data")

// writeSSE writes one `data: <payload>` frame.
func writeSSE(writer io.Writer, payload []byte) error {
	frame := make([]byte, 0, len(payload)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	_, errWrite := writer.Write(frame)
	return errWrite
}

// streamChatCompletion normalizes an upstream SSE stream into standard
// chat.completion.chunk events and terminates it with `data: [DONE]`.
func streamChatCompletion(ctx context.Context, src io.Reader, dst io.Writer) error {
	if src == nil {
		return errEmptyStream
	}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	firstID := ""
	model := ""
	created := int64(0)
	seenToolName := map[int]bool{}
	sawFrame := false
	sawFinish := false
	sawError := false

	for scanner.Scan() {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !strings.HasPrefix(trimmed, "data:") {
			// Drop comments and non-data lines: clients only parse data frames.
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "[DONE]" {
			break
		}
		if payload == "" {
			continue
		}

		// Error frames are forwarded unchanged: the upstream's own message is
		// more useful than anything we could reconstruct.
		if isErrorFrame(payload) {
			sawError = true
			if errWrite := writeSSE(dst, []byte(payload)); errWrite != nil {
				return errWrite
			}
			continue
		}

		var frame map[string]any
		if errUnmarshal := json.Unmarshal([]byte(payload), &frame); errUnmarshal != nil {
			sawFrame = true
			if errWrite := writeSSE(dst, []byte(payload)); errWrite != nil {
				return errWrite
			}
			continue
		}

		// function.name belongs to the first fragment only; accumulating
		// clients would otherwise build "BashBash".
		stripToolCallNames(frame, seenToolName)

		if id := stringField(frame, "id"); id != "" {
			if firstID == "" {
				firstID = id
			}
		} else if firstID != "" {
			frame["id"] = firstID
		}
		if got := stringField(frame, "model"); got != "" {
			model = got
		}
		if value, okValue := toInt64(frame["created"]); okValue {
			created = value
		}
		if finishReasonOf(frame) != "" {
			sawFinish = true
		}

		normalized, errMarshal := json.Marshal(normalizeChunkFrame(frame))
		if errMarshal != nil {
			continue
		}
		sawFrame = true
		if errWrite := writeSSE(dst, normalized); errWrite != nil {
			return errWrite
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return fmt.Errorf("workbuddy read upstream stream: %w", errScan)
	}
	if !sawFrame && !sawError {
		// The HTTP response was 2xx but carried no usable events. Surface it as
		// an SSE error frame because the status has already been committed.
		errFrame := `{"error":{"message":"upstream stream contained no data","type":"upstream_error"}}`
		if errWrite := writeSSE(dst, []byte(errFrame)); errWrite != nil {
			return errWrite
		}
	}
	if sawFrame && !sawFinish && !sawError {
		// Some upstream paths end without a finish_reason; clients rely on it
		// to stop waiting for more deltas.
		if created == 0 {
			created = time.Now().Unix()
		}
		final := map[string]any{
			"id":      firstNonEmpty(firstID, "chatcmpl-wb2api"),
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		}
		encoded, errMarshal := json.Marshal(final)
		if errMarshal == nil {
			if errWrite := writeSSE(dst, encoded); errWrite != nil {
				return errWrite
			}
		}
	}
	return writeSSE(dst, []byte("[DONE]"))
}

// normalizeChunkFrame whitelists chunk fields and normalizes choices.
func normalizeChunkFrame(frame map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"id", "object", "created", "model", "system_fingerprint", "service_tier"} {
		if value, okValue := frame[key]; okValue {
			out[key] = value
		}
	}
	if _, okObject := out["object"]; !okObject {
		out["object"] = "chat.completion.chunk"
	}
	if _, okID := out["id"]; !okID {
		out["id"] = "chatcmpl-wb2api"
	}

	if choices, okChoices := frame["choices"].([]any); okChoices {
		out["choices"] = normalizeChoices(choices)
	} else {
		out["choices"] = []any{}
	}

	if usage, okUsage := frame["usage"]; okUsage {
		out["usage"] = usage
	} else {
		out["usage"] = nil
	}
	return out
}

func normalizeChoices(choices []any) []any {
	out := make([]any, 0, len(choices))
	for _, item := range choices {
		choice, okChoice := item.(map[string]any)
		if !okChoice {
			continue
		}
		normalized := map[string]any{}
		if index, okIndex := choice["index"]; okIndex {
			normalized["index"] = index
		}
		if delta, okDelta := choice["delta"].(map[string]any); okDelta {
			normalized["delta"] = normalizeDelta(delta)
		}
		if reason := stringField(choice, "finish_reason"); reason != "" {
			normalized["finish_reason"] = reason
		} else {
			normalized["finish_reason"] = nil
		}
		out = append(out, normalized)
	}
	return out
}

func normalizeDelta(delta map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"role", "content", "reasoning_content", "refusal"} {
		if value, okValue := delta[key]; okValue {
			out[key] = value
		}
	}
	if calls, okCalls := delta["tool_calls"].([]any); okCalls && len(calls) > 0 {
		out["tool_calls"] = calls
	}
	if functionCall, okFunctionCall := delta["function_call"].(map[string]any); okFunctionCall && len(functionCall) > 0 {
		out["function_call"] = functionCall
	}
	return out
}

// stripToolCallNames removes function.name from every fragment but the first
// one per tool-call index.
func stripToolCallNames(frame map[string]any, seen map[int]bool) {
	choices, okChoices := frame["choices"].([]any)
	if !okChoices {
		return
	}
	for _, item := range choices {
		choice, okChoice := item.(map[string]any)
		if !okChoice {
			continue
		}
		delta, okDelta := choice["delta"].(map[string]any)
		if !okDelta {
			continue
		}
		calls, okCalls := delta["tool_calls"].([]any)
		if !okCalls {
			continue
		}
		for _, callItem := range calls {
			call, okCall := callItem.(map[string]any)
			if !okCall {
				continue
			}
			function, okFunction := call["function"].(map[string]any)
			if !okFunction {
				continue
			}
			index := intFromAny(call["index"])
			if seen[index] {
				delete(function, "name")
				continue
			}
			if _, okName := function["name"]; okName {
				seen[index] = true
			}
		}
	}
}

// isErrorFrame reports whether a payload carries a non-null error field.
func isErrorFrame(payload string) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if errUnmarshal := json.Unmarshal([]byte(payload), &probe); errUnmarshal != nil {
		return false
	}
	return len(probe.Error) > 0 && string(probe.Error) != "null"
}

// finishReasonOf returns the first non-empty finish_reason in a frame.
func finishReasonOf(frame map[string]any) string {
	choices, okChoices := frame["choices"].([]any)
	if !okChoices || len(choices) == 0 {
		return ""
	}
	choice, okChoice := choices[0].(map[string]any)
	if !okChoice {
		return ""
	}
	return stringField(choice, "finish_reason")
}

func toInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case json.Number:
		parsed, errParse := typed.Int64()
		return parsed, errParse == nil
	}
	return 0, false
}

// aggregateChatCompletion folds an upstream SSE stream into a single
// chat.completion JSON document (non-stream clients).
func aggregateChatCompletion(src io.Reader) ([]byte, error) {
	if src == nil {
		return nil, errEmptyStream
	}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var (
		id           string
		model        string
		created      int64
		content      strings.Builder
		reasoning    strings.Builder
		toolCalls    []any
		usage        map[string]any
		finishReason string
		sawDone      bool
		validEvents  int
		gotContent   bool
	)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		if payload == "" {
			continue
		}
		var frame map[string]any
		if errUnmarshal := json.Unmarshal([]byte(payload), &frame); errUnmarshal != nil {
			continue
		}
		if isErrorFrame(payload) {
			return nil, upstreamError(http.StatusBadGateway, payload, nil)
		}
		validEvents++

		if got := stringField(frame, "id"); got != "" && id == "" {
			id = got
		}
		if got := stringField(frame, "model"); got != "" {
			model = got
		}
		if value, okValue := toInt64(frame["created"]); okValue {
			created = value
		}
		if payloadUsage, okUsage := frame["usage"].(map[string]any); okUsage && len(payloadUsage) > 0 {
			usage = payloadUsage
		}

		choices, okChoices := frame["choices"].([]any)
		if !okChoices || len(choices) == 0 {
			continue
		}
		choice, okChoice := choices[0].(map[string]any)
		if !okChoice {
			continue
		}
		if reason := stringField(choice, "finish_reason"); reason != "" {
			finishReason = reason
		}

		// Some upstream frames put content in message rather than delta; the
		// gotContent latch prevents counting the same text twice.
		if delta, okDelta := choice["delta"].(map[string]any); okDelta {
			appendContent(&content, delta["content"])
			appendContent(&reasoning, delta["reasoning_content"])
			if calls, okCalls := delta["tool_calls"].([]any); okCalls && len(calls) > 0 {
				toolCalls = mergeToolCalls(toolCalls, calls)
				gotContent = true
			}
		}
		if message, okMessage := choice["message"].(map[string]any); okMessage && !gotContent {
			if text, okText := message["content"].(string); okText && text != "" {
				content.WriteString(text)
			}
			if text, okText := message["reasoning_content"].(string); okText && text != "" {
				reasoning.WriteString(text)
			}
		}
		if content.Len() > 0 {
			gotContent = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("workbuddy read upstream stream: %w", errScan)
	}
	if validEvents == 0 {
		return nil, errEmptyStream
	}

	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		// Truncated output means tool arguments are incomplete; drop them.
		if finishReason == "length" || !sawDone {
			toolCalls = dropTruncatedToolCalls(toolCalls)
		}
		if len(toolCalls) > 0 {
			message["tool_calls"] = sortToolCalls(toolCalls)
		}
	}
	if strings.TrimSpace(finishReason) == "" {
		finishReason = "stop"
	}

	response := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if usage != nil {
		response["usage"] = ensureUsageTotal(usage)
	}
	return json.Marshal(response)
}

func appendContent(builder *strings.Builder, value any) {
	if text, okText := value.(string); okText && text != "" {
		builder.WriteString(text)
	}
}

// mergeToolCalls merges tool_call fragments by index. Fragments without an
// index continue the most recent call (an occasional upstream behavior).
func mergeToolCalls(existing []any, incoming []any) []any {
	byIndex := make(map[int]map[string]any, len(existing))
	order := make([]int, 0, len(existing)+len(incoming))
	for _, item := range existing {
		call, okCall := item.(map[string]any)
		if !okCall {
			continue
		}
		index := intFromAny(call["index"])
		byIndex[index] = call
		order = append(order, index)
	}

	nextIndex := len(order)
	for _, item := range incoming {
		call, okCall := item.(map[string]any)
		if !okCall {
			continue
		}
		key := nextIndex
		if _, okIndex := call["index"]; okIndex {
			key = intFromAny(call["index"])
		} else if len(order) > 0 {
			key = order[len(order)-1]
		} else {
			nextIndex++
		}
		target, okTarget := byIndex[key]
		if !okTarget {
			target = map[string]any{"index": key}
			byIndex[key] = target
			order = append(order, key)
		}
		mergeToolCallFields(target, call)
	}

	out := make([]any, 0, len(order))
	for _, key := range order {
		if call, okCall := byIndex[key]; okCall {
			out = append(out, call)
		}
	}
	return out
}

func mergeToolCallFields(target, incoming map[string]any) {
	if id := stringField(incoming, "id"); id != "" {
		target["id"] = id
	}
	if callType := stringField(incoming, "type"); callType != "" {
		target["type"] = callType
	}
	incomingFunction, okFunction := incoming["function"].(map[string]any)
	if !okFunction {
		return
	}
	targetFunction, okTargetFunction := target["function"].(map[string]any)
	if !okTargetFunction {
		targetFunction = map[string]any{}
		target["function"] = targetFunction
	}
	if name := stringField(incomingFunction, "name"); name != "" {
		targetFunction["name"] = name
	}
	if arguments, okArgs := incomingFunction["arguments"].(string); okArgs && arguments != "" {
		previous, _ := targetFunction["arguments"].(string)
		targetFunction["arguments"] = previous + arguments
	}
}

// sortToolCalls orders tool calls by index (clients depend on the order).
func sortToolCalls(calls []any) []any {
	out := append([]any(nil), calls...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			left, _ := out[j-1].(map[string]any)
			right, _ := out[j].(map[string]any)
			if intFromAny(left["index"]) <= intFromAny(right["index"]) {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// isTruncatedArguments reports whether tool arguments are incomplete JSON.
// Empty arguments are valid (no-argument tools).
func isTruncatedArguments(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	var value any
	return json.Unmarshal([]byte(trimmed), &value) != nil
}

// dropTruncatedToolCalls removes tool calls whose arguments are invalid JSON.
func dropTruncatedToolCalls(calls []any) []any {
	out := make([]any, 0, len(calls))
	for _, item := range calls {
		call, okCall := item.(map[string]any)
		if !okCall {
			out = append(out, item)
			continue
		}
		function, okFunction := call["function"].(map[string]any)
		if !okFunction {
			out = append(out, call)
			continue
		}
		arguments, _ := function["arguments"].(string)
		if isTruncatedArguments(arguments) {
			continue
		}
		out = append(out, call)
	}
	return out
}

// ensureUsageTotal fills a missing total_tokens without mutating the input.
func ensureUsageTotal(usage map[string]any) map[string]any {
	if _, okTotal := usage["total_tokens"]; okTotal {
		return usage
	}
	prompt, okPrompt := toInt64(usage["prompt_tokens"])
	completion, okCompletion := toInt64(usage["completion_tokens"])
	if !okPrompt || !okCompletion {
		return usage
	}
	out := make(map[string]any, len(usage)+1)
	for key, value := range usage {
		out[key] = value
	}
	out["total_tokens"] = prompt + completion
	return out
}
