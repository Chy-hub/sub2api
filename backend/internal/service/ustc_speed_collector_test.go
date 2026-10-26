package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestUSTCSpeedCollectorChatCompletionsFragmentsAndLateUsage(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start.Add(100*time.Millisecond), 200, "text/event-stream; charset=utf-8")

	first := `data: {"id":"x","model":"qwen-test","choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`
	c.Observe([]byte(first[:31]), start.Add(500*time.Millisecond))
	c.Observe([]byte(first[31:]+"\n\n"), start.Add(500*time.Millisecond))
	secondAndFinish := "data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	c.Observe([]byte(secondAndFinish), start.Add(1700*time.Millisecond))
	// A trailer with usage arrives after the last content. It must not move the
	// end of the output phase or shorten the measured tail.
	c.Observe([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":3},\"cost\":0.004}}\n\n"), start.Add(2400*time.Millisecond))
	obs := c.Finish(start.Add(3000*time.Millisecond), nil)

	assertFloat(t, obs.HeadersMS, 100)
	assertFloat(t, obs.TotalMS, 3000)
	assertFloat(t, obs.FirstOutputMS, 500)
	assertFloat(t, obs.TTFTMS, 500)
	assertFloat(t, obs.OutputMS, 1200)
	assertFloat(t, obs.TailMS, 1300)
	assertFloat(t, obs.MaxGapMS, 1200)
	assertFloat(t, obs.GenerationTPS, 7.0/2.5)
	assertFloat(t, obs.OverallTPS, 7.0/3.0)
	if obs.EventCount != 2 || obs.OutputChars != 11 {
		t.Fatalf("content accounting = events %d chars %d, want 2 / 11", obs.EventCount, obs.OutputChars)
	}
	if obs.GapOver1S != 1 || obs.GapOver2S != 0 {
		t.Fatalf("gap thresholds = >1s:%d >2s:%d, want 1 / 0", obs.GapOver1S, obs.GapOver2S)
	}
	if obs.InputTokens != 11 || obs.OutputTokens != 7 || obs.CachedTokens != 3 || !obs.InputTokensKnown || !obs.TokenCountKnown {
		t.Fatalf("usage not collected: %+v", obs)
	}
	if !obs.StreamDone || obs.FinishReason != "stop" || obs.ObservedModel != "qwen-test" {
		t.Fatalf("terminal metadata not collected: %+v", obs)
	}
	assertClosed(t, c.FirstOutput())
	if obs.MeasurementMode != "stream_approx" {
		t.Fatalf("measurement mode = %q", obs.MeasurementMode)
	}
}

func TestUSTCSpeedCollectorCountsLongOutputGaps(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	for i, at := range []time.Duration{100 * time.Millisecond, 1200 * time.Millisecond, 3400 * time.Millisecond} {
		frame := fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":\"%d\"}\n\n", i)
		c.Observe([]byte(frame), start.Add(at))
	}
	obs := c.Finish(start.Add(3500*time.Millisecond), nil)
	if obs.GapOver1S != 2 || obs.GapOver2S != 1 {
		t.Fatalf("gap thresholds = >1s:%d >2s:%d, want 2 / 1", obs.GapOver1S, obs.GapOver2S)
	}
	assertFloat(t, obs.MaxGapMS, 2200)
}

func TestUSTCSpeedCollectorIgnoresLifecycleAndEmptyEvents(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	body := ": ping\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":0}}\n\n" +
		"data: [DONE]\n\n"
	c.Observe([]byte(body), start.Add(time.Second))
	select {
	case <-c.FirstOutput():
		t.Fatal("lifecycle-only events closed first-output signal")
	default:
	}
	obs := c.Finish(start.Add(2*time.Second), nil)
	if obs.EventCount != 0 || obs.OutputChars != 0 || obs.FirstOutputMS != nil || obs.TTFTMS != nil {
		t.Fatalf("empty events produced output metrics: %+v", obs)
	}
	if !obs.StreamDone || !obs.TokenCountKnown || obs.OutputTokens != 0 {
		t.Fatalf("usage or protocol completion missing: %+v", obs)
	}
}

func TestUSTCSpeedCollectorSSELineEndings(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\r\r"), start.Add(100*time.Millisecond))
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"b\"}\r\n\r\n"), start.Add(200*time.Millisecond))
	obs := c.Finish(start.Add(300*time.Millisecond), nil)
	if obs.EventCount != 2 || obs.OutputChars != 2 {
		t.Fatalf("CR/CRLF SSE line endings were not parsed: %+v", obs)
	}
}

func TestUSTCSpeedCollectorReasoningAndHiddenReasoning(t *testing.T) {
	t.Run("reasoning only is valid first output but not body TTFT", func(t *testing.T) {
		start := time.Now()
		c := NewUSTCSpeedCollector(start, true)
		c.Headers(start, 200, "text/event-stream")
		c.Observe([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n"), start.Add(200*time.Millisecond))
		c.Observe([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" more\"}}]}\n\n"), start.Add(1200*time.Millisecond))
		c.Observe([]byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":5,\"completion_tokens_details\":{\"reasoning_tokens\":5}}}\n\n"), start.Add(1300*time.Millisecond))
		obs := c.Finish(start.Add(1400*time.Millisecond), nil)
		if obs.FirstOutputMS == nil || *obs.FirstOutputMS != 200 || obs.TTFTMS != nil {
			t.Fatalf("first-output / body TTFT = %+v / %+v", obs.FirstOutputMS, obs.TTFTMS)
		}
		if obs.OutputChars != 0 || obs.ReasoningChars != 10 || obs.EventCount != 2 {
			t.Fatalf("reasoning accounting = %+v", obs)
		}
		assertClosed(t, c.FirstOutput())
		assertFloat(t, obs.GenerationTPS, 5.0/1.2)
	})

	t.Run("usage shows hidden reasoning so body phase is not mislabeled", func(t *testing.T) {
		start := time.Now()
		c := NewUSTCSpeedCollector(start, true)
		c.Headers(start, 200, "text/event-stream")
		c.Observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"), start.Add(400*time.Millisecond))
		c.Observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" continued\"}}]}\n\n"), start.Add(600*time.Millisecond))
		c.Observe([]byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":9,\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\n"), start.Add(800*time.Millisecond))
		obs := c.Finish(start.Add(time.Second), nil)
		assertFloat(t, obs.GenerationTPS, 15) // Common first-output-to-end approximation.
		if obs.ReasoningTokens != 4 {
			t.Fatal("raw reasoning usage was lost")
		}
		if obs.OverallTPS == nil {
			t.Fatal("overall TPS should still use total request duration")
		}
	})
}

func TestUSTCSpeedCollectorDoesNotInventTPSForOneToken(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n"), start.Add(200*time.Millisecond))
	c.Observe([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":1}}}\n\n"), start.Add(500*time.Millisecond))
	obs := c.Finish(start.Add(time.Second), nil)
	if obs.GenerationTPS != nil || obs.OverallTPS != nil {
		t.Fatalf("one token produced a misleading rate: generation=%v overall=%v", obs.GenerationTPS, obs.OverallTPS)
	}
}

func TestUSTCSpeedCollectorResponsesProtocol(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start.Add(50*time.Millisecond), 200, "text/event-stream")
	frames := []string{
		`data: {"type":"response.created","response":{"model":"gpt-test"}}` + "\n\n",
		`event: response.reasoning_summary_text.delta` + "\n" + `data: {"type":"response.reasoning_summary_text.delta","delta":"plan"}` + "\n\n",
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n",
		`data: {"type":"response.output_text.delta","delta":" world"}` + "\n\n",
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":13,"output_tokens":6,"output_tokens_details":{"reasoning_tokens":2,"cached_tokens":1},"cost":0.02}}}` + "\n\n",
	}
	c.Observe([]byte(strings.Join(frames, "")), start.Add(time.Second))
	obs := c.Finish(start.Add(2*time.Second), nil)
	if obs.EventCount != 3 || obs.OutputChars != 11 || obs.ReasoningChars != 4 {
		t.Fatalf("Responses content accounting = %+v", obs)
	}
	if obs.ObservedModel != "gpt-test" || obs.FinishReason != "completed" || !obs.StreamDone {
		t.Fatalf("Responses metadata = %+v", obs)
	}
	if obs.InputTokens != 13 || obs.OutputTokens != 6 || obs.ReasoningTokens != 2 || obs.CachedTokens != 1 {
		t.Fatalf("Responses usage = %+v", obs)
	}
	assertClosed(t, c.FirstOutput())
}

func TestUSTCSpeedCollectorAnthropicProtocol(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	frames := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-test\",\"usage\":{\"input_tokens\":21,\"cache_read_input_tokens\":3,\"cache_creation_input_tokens\":2}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"thinking\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"think\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	c.Observe([]byte(strings.Join(frames, "")), start.Add(time.Second))
	obs := c.Finish(start.Add(1500*time.Millisecond), nil)
	if !obs.StreamDone || obs.FinishReason != "end_turn" || obs.ObservedModel != "claude-test" {
		t.Fatalf("Anthropic metadata = %+v", obs)
	}
	if obs.InputTokens != 21 || obs.OutputTokens != 8 || obs.CachedTokens != 5 {
		t.Fatalf("Anthropic usage = %+v", obs)
	}
	if obs.ReasoningChars != 5 || obs.OutputChars != 4 || obs.EventCount != 3 {
		t.Fatalf("Anthropic content accounting = %+v", obs)
	}
}

func TestUSTCSpeedCollectorErrorCancellationAndPartialStream(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	c.Observe([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"private answer\"}}]}\n\n"), start.Add(300*time.Millisecond))
	c.Observe([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"upstream_busy\",\"code\":\"busy_429\",\"message\":\"do not retain this private error text\"}}\n\n"), start.Add(500*time.Millisecond))
	obs := c.Finish(start.Add(700*time.Millisecond), context.Canceled)
	if obs.StreamDone {
		t.Fatal("cancelled stream without protocol terminal marker marked done")
	}
	if obs.ErrorCode != "busy_429" || obs.EventCount != 1 || obs.OutputChars != len("private answer") {
		t.Fatalf("error/cancellation metrics = %+v", obs)
	}
	encoded, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private answer", "do not retain", "private error text"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("observation retained response text %q: %s", secret, encoded)
		}
	}
	assertFloat(t, obs.TotalMS, 700)
	assertFloat(t, obs.TailMS, 400)

	// EOF preserves partial measurements but does not imply [DONE].
	partial := NewUSTCSpeedCollector(start, true)
	partial.Headers(start, 200, "text/event-stream")
	partial.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}"), start.Add(100*time.Millisecond))
	partialObs := partial.Finish(start.Add(200*time.Millisecond), nil)
	if partialObs.StreamDone || partialObs.OutputChars != 1 {
		t.Fatalf("partial stream was lost or treated as complete: %+v", partialObs)
	}
}

func TestUSTCSpeedCollectorOversizedInputDoesNotAbortSampling(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	large := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"" + strings.Repeat("x", maxUSTCSSEventBytes) + "\"}\n\n"
	c.Observe([]byte(large), start.Add(100*time.Millisecond))
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"y\"}\n\n"), start.Add(200*time.Millisecond))
	obs := c.Finish(start.Add(300*time.Millisecond), nil)
	if obs.OutputChars != 1 || obs.EventCount != 1 {
		t.Fatalf("oversized event was retained or poisoned later events: %+v", obs)
	}

	jsonBody := `{"model":"x","usage":{"input_tokens":1,"output_tokens":2}}`
	buffered := NewUSTCSpeedCollector(start, false)
	buffered.Headers(start, 200, "application/json")
	buffered.Observe([]byte(jsonBody+strings.Repeat(" ", maxUSTCBodyBytes)), start.Add(100*time.Millisecond))
	bodyObs := buffered.Finish(start.Add(200*time.Millisecond), nil)
	if bodyObs.TotalMS == nil || bodyObs.TokenCountKnown {
		t.Fatalf("oversized non-stream body should remain bounded and unparsed: %+v", bodyObs)
	}
}

func TestUSTCSpeedCollectorNonStreamingOnlyRecordsTotalAndUsage(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, false)
	c.Headers(start.Add(120*time.Millisecond), 200, "application/json")
	c.Observe([]byte(`{"id":"r","model":"deepseek-test","choices":[{"message":{"content":"secret"},"finish_reason":"stop"}],"usage":{"prompt_tokens":90,"completion_tokens":12,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":2},"cost":"0.01"}}`), start.Add(130*time.Millisecond))
	obs := c.Finish(start.Add(900*time.Millisecond), nil)
	assertFloat(t, obs.HeadersMS, 120)
	assertFloat(t, obs.TotalMS, 900)
	if obs.FirstOutputMS != nil || obs.TTFTMS != nil || obs.OutputMS != nil || obs.TailMS != nil || obs.GenerationTPS != nil || obs.OverallTPS != nil {
		t.Fatalf("non-stream response exposed stage metrics: %+v", obs)
	}
	if !obs.InputTokensKnown || !obs.TokenCountKnown || obs.InputTokens != 90 || obs.OutputTokens != 12 || obs.ReasoningTokens != 2 || obs.CachedTokens != 8 {
		t.Fatalf("non-stream usage missing: %+v", obs)
	}
	if obs.OutputChars != len("secret") || obs.EventCount != 0 || !obs.StreamDone || obs.MeasurementMode != "non_stream" {
		t.Fatalf("unexpected non-stream output accounting: %+v", obs)
	}
	assertFloat(t, obs.ProviderCost, 0.01)
	if obs.FinishReason != "stop" || obs.ObservedModel != "deepseek-test" {
		t.Fatalf("non-stream metadata = %+v", obs)
	}
}

func TestUSTCSpeedCollectorNonStreamingContentWithoutUsage(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, false)
	c.Headers(start.Add(50*time.Millisecond), 200, "application/json")
	c.Observe([]byte(`{"choices":[{"message":{"content":"content only"}}]}`), start.Add(100*time.Millisecond))
	obs := c.Finish(start.Add(300*time.Millisecond), io.EOF)
	if obs.OutputChars != len("content only") || obs.ReasoningChars != 0 || obs.EventCount != 0 || obs.StreamDone != true {
		t.Fatalf("non-stream content was not detected: %+v", obs)
	}
	if obs.InputTokensKnown || obs.TokenCountKnown {
		t.Fatalf("missing usage was reported as known: %+v", obs)
	}
	if obs.FirstOutputMS != nil || obs.TTFTMS != nil || obs.OutputMS != nil || obs.TailMS != nil || obs.GenerationTPS != nil || obs.OverallTPS != nil {
		t.Fatalf("non-stream response exposed stage metrics: %+v", obs)
	}
}

func TestUSTCSpeedCollectorResponseFailedWithoutErrorCode(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"), start.Add(100*time.Millisecond))
	c.Observe([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n"), start.Add(200*time.Millisecond))
	obs := c.Finish(start.Add(300*time.Millisecond), io.EOF)
	if !obs.StreamDone || obs.ErrorCode != "response_failed" || obs.EventCount != 1 {
		t.Fatalf("failed Responses event was not preserved as an error: %+v", obs)
	}
}

func TestUSTCSpeedCollectorFirstOutputAndFinishAreConcurrentSafe(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 200, "text/event-stream")
	waiting := make(chan struct{})
	go func() {
		<-c.FirstOutput()
		close(waiting)
	}()
	c.Observe([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"), start.Add(time.Millisecond))
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("first-output wait did not unblock")
	}
	var first USTCSpeedObservation
	var second USTCSpeedObservation
	done := make(chan struct{}, 2)
	go func() { first = c.Finish(start.Add(2*time.Millisecond), nil); done <- struct{}{} }()
	go func() { second = c.Finish(start.Add(3*time.Millisecond), nil); done <- struct{}{} }()
	<-done
	<-done
	if first.TotalMS == nil || second.TotalMS == nil || *first.TotalMS != *second.TotalMS {
		t.Fatalf("Finish was not idempotent: first=%+v second=%+v", first, second)
	}
	wantTotal := *first.TotalMS
	// The returned pointers are caller-owned snapshots, not aliases into the
	// cached idempotent result.
	*first.TotalMS = 999
	third := c.Finish(start.Add(4*time.Millisecond), nil)
	if third.TotalMS == nil || *third.TotalMS != wantTotal {
		t.Fatalf("caller mutation changed cached result: %+v", third)
	}
}

func TestUSTCSpeedCollectorStreamingJSONErrorBody(t *testing.T) {
	start := time.Now()
	c := NewUSTCSpeedCollector(start, true)
	c.Headers(start, 429, "application/json")
	c.Observe([]byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"private"}}`), start.Add(20*time.Millisecond))
	obs := c.Finish(start.Add(30*time.Millisecond), nil)
	if obs.ErrorCode != "rate_limit_exceeded" || obs.StreamDone || obs.TotalMS == nil {
		t.Fatalf("stream JSON error response = %+v", obs)
	}
}

func assertFloat(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil || mathAbs(*got-want) > 0.000001 {
		t.Fatalf("value = %v, want %v", got, want)
	}
}

func assertClosed(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal("first-output channel is not closed")
	}
}

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
