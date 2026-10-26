package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"mime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

const (
	maxUSTCSSEventBytes = 1 << 20
	maxUSTCBodyBytes    = 1 << 20
)

// USTCSpeedCollector incrementally extracts timing and usage data from an
// upstream response. It intentionally never retains generated text after the
// current bounded SSE event has been parsed.
type USTCSpeedCollector struct {
	mu sync.Mutex

	start       time.Time
	streaming   bool
	streamSSE   bool
	statusCode  int
	headersSeen bool
	headersMS   *float64
	firstSignal chan struct{}
	firstClosed bool
	finished    bool
	final       USTCSpeedObservation

	firstOutputAt  time.Time
	firstBodyAt    time.Time
	lastOutputAt   time.Time
	hasOutput      bool
	hasBody        bool
	outputEvents   int
	maxGap         time.Duration
	gapCount       int
	gapOver1S      int
	gapOver2S      int
	outputChars    int
	reasoningChars int
	finishReason   string
	observedModel  string
	errorCode      string
	streamDone     bool
	providerCost   *float64

	inputTokens       int
	outputTokens      int
	reasoningTokens   int
	cachedTokens      int
	inputTokensKnown  bool
	outputTokensKnown bool
	reasoningKnown    bool
	cachedKnown       bool

	line          []byte
	lineOverflow  bool
	skipLF        bool
	eventData     []byte
	eventName     string
	eventBytes    int
	eventTooLarge bool
	eventHasData  bool

	body         []byte
	bodyTooLarge bool
}

// NewUSTCSpeedCollector starts a bounded response observer. The provided
// start time should come from time.Now so its monotonic clock reading is kept.
func NewUSTCSpeedCollector(start time.Time, streaming bool) *USTCSpeedCollector {
	return &USTCSpeedCollector{
		start:       start,
		streaming:   streaming,
		streamSSE:   streaming,
		firstSignal: make(chan struct{}),
	}
}

// Headers records the first response-header arrival. If a streaming request
// receives a JSON error or another non-SSE response, its bounded body is
// parsed for safe error and usage metadata instead.
func (c *USTCSpeedCollector) Headers(at time.Time, statusCode int, contentType string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.headersSeen {
		return
	}
	c.headersSeen = true
	c.statusCode = statusCode
	if elapsed, ok := ustcElapsedMS(c.start, at); ok {
		c.headersMS = &elapsed
	}
	if !c.streaming {
		c.streamSSE = false
		return
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err == nil && strings.EqualFold(mediaType, "text/event-stream") {
		c.streamSSE = true
		return
	}
	if statusCode >= 400 || (err == nil && mediaType != "") {
		c.streamSSE = false
	}
}

// Observe passes one arbitrary transport read to the collector. Streaming
// reads may split an SSE field or contain many events. Non-streaming and
// non-SSE error bodies are capped at 1 MiB.
func (c *USTCSpeedCollector) Observe(data []byte, at time.Time) {
	if c == nil || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished {
		return
	}
	if c.streaming && c.streamSSE {
		c.observeSSELocked(data, at)
		return
	}
	if c.bodyTooLarge {
		return
	}
	remaining := maxUSTCBodyBytes - len(c.body)
	if len(data) > remaining {
		if remaining > 0 {
			c.body = append(c.body, data[:remaining]...)
		}
		c.bodyTooLarge = true
		return
	}
	c.body = append(c.body, data...)
}

// Finish finalizes once and returns an independent snapshot on every call.
// io.EOF is the normal transport end; StreamDone is instead derived from the
// response protocol's explicit terminal marker.
func (c *USTCSpeedCollector) Finish(at time.Time, readErr error) USTCSpeedObservation {
	if c == nil {
		return USTCSpeedObservation{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.finished {
		if c.streaming && c.streamSSE {
			c.flushSSELocked(at)
		} else if !c.bodyTooLarge {
			c.parseBufferedBodyLocked(at, readErr)
		}
		clear(c.body)
		c.body = nil
		clear(c.eventData)
		c.eventData = nil
		clear(c.line)
		c.line = nil
		if readErr != nil && !errors.Is(readErr, context.Canceled) && !errors.Is(readErr, context.DeadlineExceeded) && !errors.Is(readErr, io.EOF) {
			if c.errorCode == "" {
				c.errorCode = "read_error"
			}
		} else if errors.Is(readErr, context.Canceled) && c.errorCode == "" {
			c.errorCode = "canceled"
		} else if errors.Is(readErr, context.DeadlineExceeded) && c.errorCode == "" {
			c.errorCode = "deadline_exceeded"
		}
		c.final = c.buildObservationLocked(at)
		c.finished = true
	}
	return cloneUSTCSpeedObservation(c.final)
}

// FirstOutput closes when the collector observes its first non-empty text,
// reasoning, or tool-argument delta. Lifecycle, role-only, and usage events do
// not close it.
func (c *USTCSpeedCollector) FirstOutput() <-chan struct{} {
	if c == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.firstSignal
}

func (c *USTCSpeedCollector) observeSSELocked(data []byte, at time.Time) {
	for _, b := range data {
		if c.skipLF {
			c.skipLF = false
			if b == '\n' {
				continue
			}
		}
		if b == '\r' {
			c.endSSELineLocked(at)
			c.skipLF = true
			continue
		}
		if b == '\n' {
			c.endSSELineLocked(at)
			continue
		}
		if c.eventBytes < maxUSTCSSEventBytes+1 {
			c.eventBytes++
		}
		if c.eventBytes > maxUSTCSSEventBytes {
			c.markSSEEventTooLargeLocked()
		}
		if c.lineOverflow {
			continue
		}
		if len(c.line) >= maxUSTCSSEventBytes {
			c.lineOverflow = true
			c.markSSEEventTooLargeLocked()
			c.line = c.line[:0]
			continue
		}
		c.line = append(c.line, b)
	}
}

func (c *USTCSpeedCollector) endSSELineLocked(at time.Time) {
	if c.eventBytes < maxUSTCSSEventBytes+1 {
		c.eventBytes++
	}
	if c.lineOverflow {
		c.eventTooLarge = true
		c.lineOverflow = false
		clear(c.line)
		c.line = c.line[:0]
		return
	}
	c.processSSELineLocked(c.line, at)
	clear(c.line)
	c.line = c.line[:0]
}

func (c *USTCSpeedCollector) markSSEEventTooLargeLocked() {
	c.eventTooLarge = true
	clear(c.eventData)
	c.eventData = c.eventData[:0]
	c.eventName = ""
	c.eventHasData = false
}

func (c *USTCSpeedCollector) processSSELineLocked(line []byte, at time.Time) {
	if len(line) == 0 {
		c.dispatchSSEEventLocked(at)
		return
	}
	if line[0] == ':' { // heartbeat/comment
		return
	}
	if line[0] == '{' || line[0] == '[' {
		tooLarge := c.eventTooLarge
		c.dispatchSSEEventLocked(at)
		if !tooLarge && len(line) <= maxUSTCSSEventBytes {
			c.processSSEDataLocked("", line, at)
		}
		return
	}
	field := line
	value := []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		field = line[:i]
		value = line[i+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	if c.eventTooLarge {
		return
	}
	switch string(field) {
	case "event":
		if len(value) <= 256 {
			c.eventName = string(value)
		}
	case "data":
		if len(value)+len(c.eventData)+1 > maxUSTCSSEventBytes {
			c.markSSEEventTooLargeLocked()
			return
		}
		if c.eventHasData {
			c.eventData = append(c.eventData, '\n')
		}
		c.eventData = append(c.eventData, value...)
		c.eventHasData = true
	}
}

func (c *USTCSpeedCollector) dispatchSSEEventLocked(at time.Time) {
	if !c.eventTooLarge && c.eventHasData {
		c.processSSEDataLocked(c.eventName, c.eventData, at)
	}
	clear(c.eventData)
	c.eventData = c.eventData[:0]
	c.eventName = ""
	c.eventBytes = 0
	c.eventTooLarge = false
	c.eventHasData = false
}

func (c *USTCSpeedCollector) flushSSELocked(at time.Time) {
	if c.lineOverflow {
		c.markSSEEventTooLargeLocked()
		c.lineOverflow = false
		c.line = c.line[:0]
	} else if len(c.line) > 0 {
		c.processSSELineLocked(c.line, at)
		clear(c.line)
		c.line = c.line[:0]
	}
	// A terminal read can end after a complete data line without the final
	// blank line. Preserve its measurements, but do not invent a terminal event.
	if c.eventHasData || c.eventTooLarge {
		c.dispatchSSEEventLocked(at)
	}
}

func (c *USTCSpeedCollector) processSSEDataLocked(eventName string, payload []byte, at time.Time) {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		c.streamDone = true
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}
	root := gjson.ParseBytes(payload)
	eventType := strings.TrimSpace(root.Get("type").String())
	if eventType == "" {
		eventType = strings.TrimSpace(eventName)
	}
	if len(eventType) > 128 {
		eventType = eventType[:128]
	}
	c.observeMetadataLocked(root)
	c.captureErrorCodeLocked(root)

	if strings.HasPrefix(eventType, "response.") {
		switch eventType {
		case "response.completed", "response.failed", "response.incomplete":
			c.streamDone = true
			c.setFinishReasonLocked(ustcFirstNonEmpty(
				root.Get("response.status").String(),
				root.Get("response.incomplete_details.reason").String(),
				strings.TrimPrefix(eventType, "response."),
			))
		}
	}
	if eventType == "message_stop" {
		c.streamDone = true
	}
	if eventType == "message_delta" {
		c.setFinishReasonLocked(root.Get("delta.stop_reason").String())
	}
	if eventType == "error" || strings.HasSuffix(eventType, ".failed") {
		if eventType == "response.failed" && c.errorCode == "" {
			c.errorCode = "response_failed"
		}
		if c.finishReason == "" {
			c.setFinishReasonLocked("error")
		}
	}

	var outputChars, reasoningChars int
	switch {
	case root.Get("choices").IsArray():
		outputChars, reasoningChars = c.extractChatDeltaLocked(root)
	case strings.HasPrefix(eventType, "response."):
		outputChars, reasoningChars = c.extractResponsesDeltaLocked(root, eventType)
	case strings.HasPrefix(eventType, "content_block_") || eventType == "message_delta" || eventType == "message_start" || eventName == "content_block_delta":
		outputChars, reasoningChars = c.extractAnthropicDeltaLocked(root, eventType)
	}
	if outputChars > 0 || reasoningChars > 0 {
		c.recordOutputLocked(outputChars, reasoningChars, at)
	}
}

func (c *USTCSpeedCollector) observeMetadataLocked(root gjson.Result) {
	for _, path := range []string{"model", "response.model", "message.model"} {
		if value := strings.TrimSpace(root.Get(path).String()); value != "" {
			if len(value) > 256 {
				value = value[:256]
			}
			c.observedModel = value
			break
		}
	}
	for _, path := range []string{"usage", "response.usage", "message.usage"} {
		usage := root.Get(path)
		if usage.Exists() && usage.IsObject() {
			c.mergeUsageLocked(usage)
		}
	}
	if value, ok := parseUSTCNumber(root.Get("cost")); ok {
		c.setProviderCostLocked(value)
	}
	if root.Get("choices").IsArray() {
		root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
			finishReason := choice.Get("finish_reason").String()
			if finishReason != "" {
				c.setFinishReasonLocked(finishReason)
				c.streamDone = true
			}
			return true
		})
	}
}

func (c *USTCSpeedCollector) mergeUsageLocked(usage gjson.Result) {
	if value, ok := firstNonNegativeInt(usage,
		"input_tokens", "prompt_tokens", "prompt_token_count"); ok {
		c.inputTokens, c.inputTokensKnown = value, true
	}
	if value, ok := firstNonNegativeInt(usage,
		"output_tokens", "completion_tokens", "completion_token_count"); ok {
		c.outputTokens, c.outputTokensKnown = value, true
	}
	if value, ok := firstNonNegativeInt(usage,
		"output_tokens_details.reasoning_tokens",
		"completion_tokens_details.reasoning_tokens",
		"reasoning_tokens"); ok {
		c.reasoningTokens, c.reasoningKnown = value, true
	}
	if cached, ok := ustcCachedTokenCount(usage); ok {
		c.cachedTokens, c.cachedKnown = cached, true
	}
	if value, ok := parseUSTCNumber(usage.Get("cost")); ok {
		c.setProviderCostLocked(value)
	}
}

func ustcCachedTokenCount(usage gjson.Result) (int, bool) {
	for _, path := range []string{"cached_tokens", "input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "output_tokens_details.cached_tokens", "completion_tokens_details.cached_tokens"} {
		if value, ok := nonNegativeInt(usage.Get(path)); ok {
			return value, true
		}
	}
	read, hasRead := firstNonNegativeInt(usage, "cache_read_input_tokens")
	created, hasCreated := firstNonNegativeInt(usage, "cache_creation_input_tokens")
	if hasRead || hasCreated {
		return safeAddInt(read, created), true
	}
	return 0, false
}

func (c *USTCSpeedCollector) extractChatDeltaLocked(root gjson.Result) (int, int) {
	var outputChars, reasoningChars int
	root.Get("choices").ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		outputChars = safeAddInt(outputChars, ustcTextChars(delta.Get("content")))
		outputChars = safeAddInt(outputChars, ustcTextChars(delta.Get("refusal")))
		reasoningChars = safeAddInt(reasoningChars, ustcTextChars(delta.Get("reasoning_content")))
		reasoningChars = safeAddInt(reasoningChars, ustcTextChars(delta.Get("reasoning")))
		reasoningChars = safeAddInt(reasoningChars, ustcTextChars(delta.Get("analysis")))
		reasoningChars = safeAddInt(reasoningChars, ustcTextChars(delta.Get("reasoning_details")))
		delta.Get("tool_calls").ForEach(func(_, tool gjson.Result) bool {
			outputChars = safeAddInt(outputChars, ustcTextChars(tool.Get("function.arguments")))
			return true
		})
		return true
	})
	return outputChars, reasoningChars
}

func (c *USTCSpeedCollector) extractResponsesDeltaLocked(root gjson.Result, eventType string) (int, int) {
	delta := root.Get("delta")
	if delta.Type != gjson.String || delta.String() == "" {
		return 0, 0
	}
	if strings.Contains(eventType, "reasoning") {
		return 0, ustcTextChars(delta)
	}
	if strings.HasSuffix(eventType, ".delta") && (strings.Contains(eventType, "text") || strings.Contains(eventType, "refusal") || strings.Contains(eventType, "function_call_arguments") || strings.Contains(eventType, "code_interpreter_call_code") || strings.Contains(eventType, "audio_transcript")) {
		return ustcTextChars(delta), 0
	}
	return 0, 0
}

func (c *USTCSpeedCollector) extractAnthropicDeltaLocked(root gjson.Result, eventType string) (int, int) {
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "text_delta":
		return ustcTextChars(delta.Get("text")), 0
	case "thinking_delta":
		return 0, ustcTextChars(delta.Get("thinking"))
	case "input_json_delta":
		return ustcTextChars(delta.Get("partial_json")), 0
	}
	// A few Anthropic-compatible gateways omit delta.type but retain the event
	// name and field shape. Keep this fallback limited to content deltas.
	if eventType == "content_block_delta" || eventType == "" {
		if text := delta.Get("text"); text.Type == gjson.String {
			return ustcTextChars(text), 0
		}
		if thinking := delta.Get("thinking"); thinking.Type == gjson.String {
			return 0, ustcTextChars(thinking)
		}
		if partial := delta.Get("partial_json"); partial.Type == gjson.String {
			return ustcTextChars(partial), 0
		}
	}
	return 0, 0
}

func (c *USTCSpeedCollector) recordOutputLocked(outputChars, reasoningChars int, at time.Time) {
	if outputChars < 0 {
		outputChars = 0
	}
	if reasoningChars < 0 {
		reasoningChars = 0
	}
	if outputChars == 0 && reasoningChars == 0 {
		return
	}
	c.outputChars = safeAddInt(c.outputChars, outputChars)
	c.reasoningChars = safeAddInt(c.reasoningChars, reasoningChars)
	c.outputEvents = safeAddInt(c.outputEvents, 1)
	if !c.hasOutput {
		c.hasOutput = true
		c.firstOutputAt = at
		c.lastOutputAt = at
		if !c.firstClosed {
			close(c.firstSignal)
			c.firstClosed = true
		}
	} else if !at.IsZero() {
		if !c.lastOutputAt.IsZero() {
			if gap := at.Sub(c.lastOutputAt); gap >= 0 {
				c.gapCount = safeAddInt(c.gapCount, 1)
				if gap > c.maxGap {
					c.maxGap = gap
				}
				if gap > time.Second {
					c.gapOver1S = safeAddInt(c.gapOver1S, 1)
				}
				if gap > 2*time.Second {
					c.gapOver2S = safeAddInt(c.gapOver2S, 1)
				}
			}
		}
		if c.lastOutputAt.IsZero() || !at.Before(c.lastOutputAt) {
			c.lastOutputAt = at
		}
	}
	if outputChars > 0 {
		if !c.hasBody {
			c.hasBody = true
			c.firstBodyAt = at
		}
	}
}

func (c *USTCSpeedCollector) parseBufferedBodyLocked(at time.Time, readErr error) {
	if len(c.body) == 0 || !gjson.ValidBytes(c.body) {
		return
	}
	root := gjson.ParseBytes(c.body)
	c.observeMetadataLocked(root)
	c.captureErrorCodeLocked(root)
	outputChars, reasoningChars := ustcNonStreamingContentChars(root)
	c.outputChars = safeAddInt(c.outputChars, outputChars)
	c.reasoningChars = safeAddInt(c.reasoningChars, reasoningChars)
	if reason := ustcFirstNonEmpty(
		root.Get("choices.0.finish_reason").String(),
		root.Get("finish_reason").String(),
		root.Get("stop_reason").String(),
		root.Get("response.status").String(),
	); reason != "" {
		c.setFinishReasonLocked(reason)
	}
	if readErr == nil || errors.Is(readErr, io.EOF) {
		if c.statusCode < 400 && c.errorCode == "" {
			c.streamDone = true
		}
	}
	_ = at // Usage and terminal metadata never move the output timestamps.
}

func (c *USTCSpeedCollector) captureErrorCodeLocked(root gjson.Result) {
	for _, path := range []string{"error.code", "response.error.code", "code", "error.type", "response.error.type", "type"} {
		value := strings.TrimSpace(root.Get(path).String())
		if value == "" || (path == "type" && value != "error") {
			continue
		}
		if safe := sanitizeUSTCCode(value); safe != "" {
			c.errorCode = safe
			return
		}
	}
}

func (c *USTCSpeedCollector) setFinishReasonLocked(value string) {
	value = sanitizeUSTCCode(strings.TrimSpace(value))
	if value != "" {
		c.finishReason = value
	}
}

func (c *USTCSpeedCollector) setProviderCostLocked(value float64) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return
	}
	cost := value
	c.providerCost = &cost
}

func (c *USTCSpeedCollector) buildObservationLocked(at time.Time) USTCSpeedObservation {
	mode := "non_stream"
	if c.streaming {
		mode = "stream_approx"
	}
	obs := USTCSpeedObservation{
		HeadersMS:            cloneFloat(c.headersMS),
		InputTokens:          c.inputTokens,
		OutputTokens:         c.outputTokens,
		ReasoningTokens:      c.reasoningTokens,
		CachedTokens:         c.cachedTokens,
		InputTokensKnown:     c.inputTokensKnown,
		TokenCountKnown:      c.outputTokensKnown,
		ReasoningTokensKnown: c.reasoningKnown,
		CachedTokensKnown:    c.cachedKnown,
		OutputChars:          c.outputChars,
		ReasoningChars:       c.reasoningChars,
		EventCount:           c.outputEvents,
		GapOver1S:            c.gapOver1S,
		GapOver2S:            c.gapOver2S,
		FinishReason:         c.finishReason,
		ObservedModel:        c.observedModel,
		ErrorCode:            c.errorCode,
		StreamDone:           c.streamDone,
		ProviderCost:         cloneFloat(c.providerCost),
		MeasurementMode:      mode,
	}
	if total, ok := ustcElapsedMS(c.start, at); ok {
		obs.TotalMS = &total
	}
	if !c.streaming {
		return obs
	}
	if c.hasOutput {
		if first, ok := ustcElapsedMS(c.start, c.firstOutputAt); ok {
			obs.FirstOutputMS = &first
		}
		if tail, ok := ustcElapsedMS(c.lastOutputAt, at); ok {
			obs.TailMS = &tail
		}
		if output, ok := ustcElapsedMS(c.firstOutputAt, c.lastOutputAt); ok {
			obs.OutputMS = &output
		}
		if c.gapCount > 0 {
			gapMS := float64(c.maxGap) / float64(time.Millisecond)
			obs.MaxGapMS = &gapMS
		}
	}
	if c.hasBody {
		if ttft, ok := ustcElapsedMS(c.start, c.firstBodyAt); ok {
			obs.TTFTMS = &ttft
		}
	}
	obs.GenerationTPS = USTCGenerationTPS(obs, c.streaming)
	if c.outputTokensKnown && c.outputTokens >= 2 {
		if total, ok := ustcElapsedMS(c.start, at); ok && total > 0 {
			tps := float64(c.outputTokens) / (total / 1000)
			if isFinitePositive(tps) {
				obs.OverallTPS = &tps
			}
		}
	}
	return obs
}

// ustcNonStreamingContentChars checks supported JSON response shapes without
// retaining their text. Non-streaming samples use this only to distinguish a
// successful content response from an empty response; no stage timing is set.
func ustcNonStreamingContentChars(root gjson.Result) (outputChars, reasoningChars int) {
	if choices := root.Get("choices"); choices.IsArray() {
		choices.ForEach(func(_, choice gjson.Result) bool {
			message := choice.Get("message")
			outputChars = safeAddInt(outputChars, ustcTextChars(message.Get("content")))
			outputChars = safeAddInt(outputChars, ustcTextChars(message.Get("refusal")))
			reasoningChars = safeAddInt(reasoningChars, ustcTextChars(message.Get("reasoning_content")))
			reasoningChars = safeAddInt(reasoningChars, ustcTextChars(message.Get("reasoning")))
			message.Get("tool_calls").ForEach(func(_, tool gjson.Result) bool {
				outputChars = safeAddInt(outputChars, ustcTextChars(tool.Get("function.arguments")))
				return true
			})
			return true
		})
	}

	if items := root.Get("output"); items.IsArray() {
		items.ForEach(func(_, item gjson.Result) bool {
			typeName := item.Get("type").String()
			switch typeName {
			case "reasoning":
				reasoningChars = safeAddInt(reasoningChars, ustcTextChars(item.Get("summary")))
			case "function_call":
				outputChars = safeAddInt(outputChars, ustcTextChars(item.Get("arguments")))
			default:
				item.Get("content").ForEach(func(_, content gjson.Result) bool {
					contentType := content.Get("type").String()
					if strings.Contains(contentType, "reasoning") {
						reasoningChars = safeAddInt(reasoningChars, ustcTextChars(content.Get("text")))
					} else {
						outputChars = safeAddInt(outputChars, ustcTextChars(content.Get("text")))
					}
					return true
				})
			}
			return true
		})
	}

	if blocks := root.Get("content"); blocks.IsArray() {
		blocks.ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "thinking":
				reasoningChars = safeAddInt(reasoningChars, ustcTextChars(block.Get("thinking")))
			case "text":
				outputChars = safeAddInt(outputChars, ustcTextChars(block.Get("text")))
			case "tool_use":
				outputChars = safeAddInt(outputChars, ustcJSONValueChars(block.Get("input")))
			}
			return true
		})
	}
	return outputChars, reasoningChars
}

// ustcJSONValueChars estimates a non-streaming structured tool argument's
// serialized size without copying it into an observation.
func ustcJSONValueChars(value gjson.Result) int {
	if !value.Exists() || value.Type == gjson.Null {
		return 0
	}
	return utf8.RuneCountInString(value.Raw)
}

func cloneUSTCSpeedObservation(value USTCSpeedObservation) USTCSpeedObservation {
	value.HeadersMS = cloneFloat(value.HeadersMS)
	value.TotalMS = cloneFloat(value.TotalMS)
	value.FirstOutputMS = cloneFloat(value.FirstOutputMS)
	value.TTFTMS = cloneFloat(value.TTFTMS)
	value.OutputMS = cloneFloat(value.OutputMS)
	value.TailMS = cloneFloat(value.TailMS)
	value.GenerationTPS = cloneFloat(value.GenerationTPS)
	value.OverallTPS = cloneFloat(value.OverallTPS)
	value.MaxGapMS = cloneFloat(value.MaxGapMS)
	value.ProviderCost = cloneFloat(value.ProviderCost)
	return value
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func ustcElapsedMS(start, end time.Time) (float64, bool) {
	if start.IsZero() || end.IsZero() {
		return 0, false
	}
	d := end.Sub(start)
	if d < 0 {
		return 0, false
	}
	ms := float64(d) / float64(time.Millisecond)
	if math.IsNaN(ms) || math.IsInf(ms, 0) {
		return 0, false
	}
	return ms, true
}

func ustcTextChars(value gjson.Result) int {
	switch value.Type {
	case gjson.String:
		return utf8.RuneCountInString(value.String())
	case gjson.JSON:
		if value.IsArray() {
			count := 0
			value.ForEach(func(_, child gjson.Result) bool {
				count = safeAddInt(count, ustcTextChars(child))
				return true
			})
			return count
		}
		if value.IsObject() {
			return ustcTextChars(value.Get("text"))
		}
	}
	return 0
}

func firstNonNegativeInt(root gjson.Result, paths ...string) (int, bool) {
	for _, path := range paths {
		if value, ok := nonNegativeInt(root.Get(path)); ok {
			return value, true
		}
	}
	return 0, false
}

func nonNegativeInt(value gjson.Result) (int, bool) {
	if !value.Exists() || value.Type == gjson.Null {
		return 0, false
	}
	var n int64
	switch value.Type {
	case gjson.Number:
		n = value.Int()
	case gjson.String:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value.String()), 10, 64)
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	if n < 0 || uint64(n) > uint64(math.MaxInt) {
		return 0, false
	}
	return int(n), true
}

func parseUSTCNumber(value gjson.Result) (float64, bool) {
	if !value.Exists() || value.Type == gjson.Null {
		return 0, false
	}
	var n float64
	switch value.Type {
	case gjson.Number:
		n = value.Float()
	case gjson.String:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value.String()), 64)
		if err != nil {
			return 0, false
		}
		n = parsed
	default:
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}

func sanitizeUSTCCode(value string) string {
	if len(value) > 128 {
		value = value[:128]
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("_.:-", r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ustcFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func safeAddInt(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	if b < 0 && a < math.MinInt-b {
		return math.MinInt
	}
	return a + b
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
