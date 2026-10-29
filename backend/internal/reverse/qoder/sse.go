package qoder

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// streamDelta is one decoded content delta from the Qoder SSE envelope.
type streamDelta struct {
	Role         string
	Content      string
	Reasoning    string
	ToolCalls    []any
	FinishReason string
	InputTokens  int
	OutputTokens int
	TotalTokens  int
	Usage        map[string]any
	Err          *reverse.UpstreamError
}

func (d streamDelta) empty() bool {
	return d.Role == "" && d.Content == "" && d.Reasoning == "" && d.ToolCalls == nil &&
		d.FinishReason == "" && d.InputTokens == 0 && d.OutputTokens == 0 && d.TotalTokens == 0 && len(d.Usage) == 0 && d.Err == nil
}

// extractDelta parses one `data:` payload of the upstream stream. The upstream
// frame is {"headers":{...},"body":"<inner CC chunk>","statusCodeValue":200};
// statusCodeValue may carry a provider failure (418/5xx) after a 200 stream
// open, and the inner document may carry a business error code.
func extractDelta(payload string) streamDelta {
	var wrapper map[string]any
	if err := json.Unmarshal([]byte(payload), &wrapper); err != nil {
		return streamDelta{}
	}
	if raw, ok := wrapper["statusCodeValue"]; ok {
		if status := envelopeStatus(raw); status != http.StatusOK {
			return streamDelta{Err: upstreamError(status, nil, []byte(envelopeDetail(wrapper)))}
		}
	}
	inner, _ := wrapper["body"].(string)
	if inner == "" {
		return streamDelta{}
	}
	var chunk map[string]any
	if err := json.Unmarshal([]byte(inner), &chunk); err != nil {
		return streamDelta{}
	}

	if value, ok := chunk["error"]; ok && value != nil {
		code, message := parseUpstreamErrorMessage([]byte(inner))
		if message == "" {
			message = "upstream error"
		}
		return streamDelta{Err: streamBusinessError(firstNonEmpty(code, "upstream_error"), message, inner)}
	}

	result := streamDelta{}
	if usage, ok := chunk["usage"].(map[string]any); ok {
		result.Usage = usage
		result.InputTokens = firstPositiveInt(intValue(usage["prompt_tokens"]), intValue(usage["input_tokens"]))
		result.OutputTokens = firstPositiveInt(intValue(usage["completion_tokens"]), intValue(usage["output_tokens"]))
		result.TotalTokens = intValue(usage["total_tokens"])
	}

	choices, _ := chunk["choices"].([]any)
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		if finish := stringValue(choice["finish_reason"]); finish != "" {
			result.FinishReason = finish
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		role := stringValue(delta["role"])
		content := rawStringValue(delta["content"])
		reasoning := rawStringValue(delta["reasoning_content"])
		var toolCalls []any
		if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
			toolCalls = calls
		}
		if role != "" || content != "" || reasoning != "" || toolCalls != nil {
			result.Role = role
			result.Content = content
			result.Reasoning = reasoning
			result.ToolCalls = toolCalls
			return result
		}
	}

	if code := stringValue(chunk["code"]); code != "" && code != "0" {
		message := firstNonEmpty(stringValue(chunk["message"]), stringValue(chunk["msg"]))
		if message == "" {
			message = fmt.Sprintf("upstream error code=%s", code)
		}
		return streamDelta{Err: streamBusinessError(code, message, inner)}
	}

	if result.FinishReason != "" || result.InputTokens > 0 || result.OutputTokens > 0 || result.TotalTokens > 0 {
		return result
	}
	return streamDelta{}
}

func envelopeStatus(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return http.StatusBadGateway
		}
		return int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return http.StatusBadGateway
		}
		return parsed
	}
	return http.StatusBadGateway
}

func envelopeDetail(wrapper map[string]any) string {
	if body, ok := wrapper["body"].(string); ok && body != "" {
		return truncateRunes(body, 400)
	}
	encoded, _ := json.Marshal(wrapper)
	return truncateRunes(string(encoded), 400)
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

// completionState aggregates deltas for a non-stream response.
type completionState struct {
	content      strings.Builder
	reasoning    strings.Builder
	toolCalls    []any
	inputTokens  int
	outputTokens int
	totalTokens  int
	finishReason string
	sawDelta     bool
	usageDetails map[string]any
}

func (s *completionState) apply(delta streamDelta) {
	if delta.empty() {
		return
	}
	s.sawDelta = true
	s.usageDetails = mergeUsage(s.usageDetails, delta.Usage)
	s.content.WriteString(delta.Content)
	s.reasoning.WriteString(delta.Reasoning)
	if delta.ToolCalls != nil {
		s.toolCalls = mergeToolCallDeltas(s.toolCalls, delta.ToolCalls)
	}
	if delta.InputTokens > 0 {
		s.inputTokens = delta.InputTokens
	}
	if delta.OutputTokens > 0 {
		s.outputTokens = delta.OutputTokens
	}
	if delta.TotalTokens > 0 {
		s.totalTokens = delta.TotalTokens
	}
	if delta.FinishReason != "" {
		s.finishReason = delta.FinishReason
	}
}

func (s *completionState) usage() map[string]any {
	total := s.totalTokens
	if total == 0 {
		total = s.inputTokens + s.outputTokens
	}
	usage := mergeUsage(nil, s.usageDetails)
	usage["prompt_tokens"] = s.inputTokens
	usage["completion_tokens"] = s.outputTokens
	usage["total_tokens"] = total
	return usage
}

func mergeUsage(existing, incoming map[string]any) map[string]any {
	if existing == nil {
		existing = map[string]any{}
	}
	for key, value := range incoming {
		existing[key] = value
	}
	return existing
}

// mergeToolCallDeltas accumulates streamed tool-call fragments by index.
func mergeToolCallDeltas(accumulated []any, incoming []any) []any {
	for _, raw := range incoming {
		call, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index := intValue(call["index"])
		var target map[string]any
		for _, item := range accumulated {
			if existing, ok := item.(map[string]any); ok && intValue(existing["index"]) == index {
				target = existing
				break
			}
		}
		if target == nil {
			target = cloneToolCall(call)
			target["index"] = index
			accumulated = append(accumulated, target)
			continue
		}
		if id := rawStringValue(call["id"]); id != "" {
			target["id"] = id
		}
		if callType := stringValue(call["type"]); callType != "" {
			target["type"] = callType
		}
		if function, ok := call["function"].(map[string]any); ok {
			targetFunction, _ := target["function"].(map[string]any)
			if targetFunction == nil {
				targetFunction = map[string]any{}
				target["function"] = targetFunction
			}
			if name := stringValue(function["name"]); name != "" {
				targetFunction["name"] = name
			}
			if arguments, ok := function["arguments"].(string); ok && arguments != "" {
				previous, _ := targetFunction["arguments"].(string)
				targetFunction["arguments"] = previous + arguments
			}
		}
	}
	return accumulated
}

func cloneToolCall(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		if function, ok := value.(map[string]any); ok && key == "function" {
			copied := make(map[string]any, len(function))
			for functionKey, functionValue := range function {
				copied[functionKey] = functionValue
			}
			out[key] = copied
			continue
		}
		out[key] = value
	}
	return out
}

// filterValidToolCalls drops calls without a function name and sorts by index.
func filterValidToolCalls(calls []any) []any {
	valid := make([]any, 0, len(calls))
	for _, item := range calls {
		call, ok := item.(map[string]any)
		if !ok {
			continue
		}
		function, ok := call["function"].(map[string]any)
		if !ok || stringValue(function["name"]) == "" {
			continue
		}
		valid = append(valid, call)
	}
	sort.SliceStable(valid, func(i, j int) bool {
		left, _ := valid[i].(map[string]any)
		right, _ := valid[j].(map[string]any)
		return intValue(left["index"]) < intValue(right["index"])
	})
	return valid
}

// buildCompletion renders the aggregated state as an OpenAI chat.completion.
func buildCompletion(state *completionState, id string, created int64, model string) []byte {
	toolCalls := filterValidToolCalls(state.toolCalls)
	finishReason := state.finishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	message := map[string]any{"role": "assistant", "content": state.content.String()}
	if state.reasoning.Len() > 0 {
		message["reasoning_content"] = state.reasoning.String()
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		if state.content.Len() == 0 {
			message["content"] = nil
		}
	}

	response := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{"index": 0, "message": message, "finish_reason": finishReason},
		},
		"usage": state.usage(),
	}
	encoded, _ := json.Marshal(response)
	return encoded
}

// streamWriter turns upstream deltas into a valid OpenAI chat.completion.chunk
// SSE stream.
type streamWriter struct {
	writer       io.Writer
	id           string
	created      int64
	model        string
	includeUsage bool

	roleSent     bool
	hasToolCalls bool
	finishReason string
	inputTokens  int
	outputTokens int
	totalTokens  int
	usageDetails map[string]any
}

func (w *streamWriter) writeChunk(delta map[string]any, finish any, choices []any, usage map[string]any) error {
	chunk := map[string]any{
		"id":      w.id,
		"object":  "chat.completion.chunk",
		"created": w.created,
		"model":   w.model,
	}
	if choices != nil {
		chunk["choices"] = choices
	} else {
		chunk["choices"] = []any{
			map[string]any{"index": 0, "delta": delta, "finish_reason": finish},
		}
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return writeSSE(w.writer, chunk)
}

func (w *streamWriter) emit(delta streamDelta) error {
	w.usageDetails = mergeUsage(w.usageDetails, delta.Usage)
	if delta.InputTokens > 0 {
		w.inputTokens = delta.InputTokens
	}
	if delta.OutputTokens > 0 {
		w.outputTokens = delta.OutputTokens
	}
	if delta.TotalTokens > 0 {
		w.totalTokens = delta.TotalTokens
	}
	if delta.FinishReason != "" {
		w.finishReason = delta.FinishReason
	}
	if delta.Content == "" && delta.Reasoning == "" && delta.ToolCalls == nil {
		return nil
	}

	payload := map[string]any{}
	if !w.roleSent {
		payload["role"] = "assistant"
		w.roleSent = true
	}
	if delta.Content != "" {
		payload["content"] = delta.Content
	}
	if delta.Reasoning != "" {
		payload["reasoning_content"] = delta.Reasoning
	}
	if delta.ToolCalls != nil {
		payload["tool_calls"] = delta.ToolCalls
		w.hasToolCalls = true
	}
	return w.writeChunk(payload, nil, nil, nil)
}

func (w *streamWriter) finish() error {
	if !w.roleSent {
		if err := w.writeChunk(map[string]any{"role": "assistant", "content": ""}, nil, nil, nil); err != nil {
			return err
		}
		w.roleSent = true
	}
	finishReason := w.finishReason
	if w.hasToolCalls {
		finishReason = "tool_calls"
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := w.writeChunk(map[string]any{}, finishReason, nil, nil); err != nil {
		return err
	}
	if w.includeUsage {
		total := w.totalTokens
		if total == 0 {
			total = w.inputTokens + w.outputTokens
		}
		usage := mergeUsage(nil, w.usageDetails)
		usage["prompt_tokens"] = w.inputTokens
		usage["completion_tokens"] = w.outputTokens
		usage["total_tokens"] = total
		if err := w.writeChunk(nil, nil, []any{}, usage); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w.writer, "data: [DONE]\n\n")
	return err
}

func writeSSE(writer io.Writer, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	return err
}

func writeStreamError(writer io.Writer, upstreamErr *reverse.UpstreamError) error {
	return writeSSE(writer, map[string]any{
		"error": map[string]any{
			"message": upstreamErr.Message,
			"type":    "upstream_error",
			"code":    upstreamErr.Code,
		},
	})
}

// consumeStream reads the upstream SSE envelope and returns the aggregated
// completion state for a non-stream response.
func consumeStream(body io.Reader) (*completionState, error) {
	state := &completionState{}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		delta := extractDelta(payload)
		if delta.Err != nil {
			return nil, delta.Err
		}
		state.apply(delta)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !state.sawDelta {
		return nil, reverse.NewUpstreamError(http.StatusBadGateway, "empty_upstream_stream", "upstream returned an empty stream")
	}
	return state, nil
}

// pumpStream proxies the upstream stream into the synthetic OpenAI SSE body.
func pumpStream(ctx context.Context, body io.ReadCloser, writer io.Writer, id string, created int64, model string, includeUsage bool) error {
	defer body.Close()

	state := &streamWriter{writer: writer, id: id, created: created, model: model, includeUsage: includeUsage}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		delta := extractDelta(payload)
		if delta.Err != nil {
			// Surface the upstream failure as an SSE error event, then abort the
			// stream instead of pretending it finished normally.
			if err := writeStreamError(writer, delta.Err); err != nil {
				return err
			}
			return delta.Err
		}
		if err := state.emit(delta); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return state.finish()
}
