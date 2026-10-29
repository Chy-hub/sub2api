package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const streamFixture = `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"glm-5.2","session_id":"secret","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"llo"}}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"Bash","arguments":"{\"path\":\""}}]}}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":"/tmp\"}"}}]}}]}

data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}

data: [DONE]

`

func dataFrames(t *testing.T, raw string) []string {
	t.Helper()
	frames := make([]string, 0, 8)
	for _, block := range strings.Split(raw, "\n\n") {
		trimmed := strings.TrimSpace(block)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "data:") {
			t.Fatalf("unexpected non-data block %q", trimmed)
		}
		frames = append(frames, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
	}
	return frames
}

// TestStreamNormalizesFrames verifies chunk rebuilding: unknown fields dropped,
// id continuation, tool name only on the first fragment, usage preserved and a
// final [DONE].
func TestStreamNormalizesFrames(t *testing.T) {
	var out bytes.Buffer
	if errStream := streamChatCompletion(context.Background(), strings.NewReader(streamFixture), &out); errStream != nil {
		t.Fatal(errStream)
	}
	frames := dataFrames(t, out.String())
	if len(frames) != 6 {
		t.Fatalf("frames = %d, want 5 chunks + DONE", len(frames))
	}
	if frames[len(frames)-1] != "[DONE]" {
		t.Fatalf("stream must end with [DONE], got %q", frames[len(frames)-1])
	}

	var first map[string]any
	if errUnmarshal := json.Unmarshal([]byte(frames[0]), &first); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if first["object"] != "chat.completion.chunk" || first["id"] != "chatcmpl-1" {
		t.Fatalf("first chunk = %v", first)
	}
	if _, okSession := first["session_id"]; okSession {
		t.Fatal("upstream-private fields must be dropped")
	}
	choices, _ := first["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	if delta["role"] != "assistant" || delta["content"] != "he" {
		t.Fatalf("first delta = %v", delta)
	}

	// The second tool fragment must not repeat function.name.
	var secondToolFragment map[string]any
	if errUnmarshal := json.Unmarshal([]byte(frames[3]), &secondToolFragment); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	toolChoices, _ := secondToolFragment["choices"].([]any)
	toolChoice, _ := toolChoices[0].(map[string]any)
	toolDelta, _ := toolChoice["delta"].(map[string]any)
	calls, _ := toolDelta["tool_calls"].([]any)
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if _, okName := function["name"]; okName {
		t.Fatal("function.name must only appear on the first fragment")
	}
	if function["arguments"] != "/tmp\"}" {
		t.Fatalf("arguments = %v", function["arguments"])
	}

	var finishFrame map[string]any
	if errUnmarshal := json.Unmarshal([]byte(frames[4]), &finishFrame); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if finishFrame["usage"] == nil {
		t.Fatal("usage must be preserved")
	}
}

// TestStreamSynthesizesFinish verifies a finish_reason is added when the
// upstream ends without one, and that [DONE] always closes the stream.
func TestStreamSynthesizesFinish(t *testing.T) {
	upstream := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	if errStream := streamChatCompletion(context.Background(), strings.NewReader(upstream), &out); errStream != nil {
		t.Fatal(errStream)
	}
	frames := dataFrames(t, out.String())
	if len(frames) != 3 {
		t.Fatalf("frames = %d, want chunk + synthetic finish + DONE", len(frames))
	}
	var final map[string]any
	if errUnmarshal := json.Unmarshal([]byte(frames[1]), &final); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	choices, _ := final["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("synthetic finish_reason = %v", choice["finish_reason"])
	}
}

// TestStreamPassesErrorFrame verifies error frames are forwarded unchanged and
// do not get a synthetic finish chunk.
func TestStreamPassesErrorFrame(t *testing.T) {
	upstream := "data: {\"error\":{\"message\":\"request illegal\",\"code\":\"11140\"}}\n\ndata: [DONE]\n\n"
	var out bytes.Buffer
	if errStream := streamChatCompletion(context.Background(), strings.NewReader(upstream), &out); errStream != nil {
		t.Fatal(errStream)
	}
	frames := dataFrames(t, out.String())
	if len(frames) != 2 || !strings.Contains(frames[0], "request illegal") {
		t.Fatalf("error frame not passed through: %v", frames)
	}
	if frames[1] != "[DONE]" {
		t.Fatalf("last frame = %q", frames[1])
	}
}

// TestStreamEmptyEmitsError verifies that a 2xx response with no data events
// still surfaces an error frame downstream instead of a silent empty stream.
func TestStreamEmptyEmitsError(t *testing.T) {
	var out bytes.Buffer
	if errStream := streamChatCompletion(context.Background(), strings.NewReader("data: [DONE]\n\n"), &out); errStream != nil {
		t.Fatal(errStream)
	}
	frames := dataFrames(t, out.String())
	if len(frames) != 2 || !strings.Contains(frames[0], "upstream stream contained no data") {
		t.Fatalf("frames = %v", frames)
	}
}

// TestAggregateMergesToolCalls verifies non-stream aggregation.
func TestAggregateMergesToolCalls(t *testing.T) {
	aggregated, errAggregate := aggregateChatCompletion(strings.NewReader(streamFixture))
	if errAggregate != nil {
		t.Fatal(errAggregate)
	}
	var completion map[string]any
	if errUnmarshal := json.Unmarshal(aggregated, &completion); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if completion["object"] != "chat.completion" || completion["id"] != "chatcmpl-1" {
		t.Fatalf("completion = %v", completion)
	}
	choices, _ := completion["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "hello" {
		t.Fatalf("content = %v", message["content"])
	}
	calls, _ := message["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("tool_calls = %v", message["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if function["name"] != "Bash" || function["arguments"] != `{"path":"/tmp"}` {
		t.Fatalf("tool call = %v", call)
	}
	usage, _ := completion["usage"].(map[string]any)
	if usage["total_tokens"] != float64(3) {
		t.Fatalf("usage = %v", usage)
	}
}

// TestAggregateDropsIncompleteToolCalls verifies truncated tool arguments are
// dropped when the stream never completed.
func TestAggregateDropsIncompleteToolCalls(t *testing.T) {
	upstream := "data: {\"id\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"Bash\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n"
	aggregated, errAggregate := aggregateChatCompletion(strings.NewReader(upstream))
	if errAggregate != nil {
		t.Fatal(errAggregate)
	}
	var completion map[string]any
	if errUnmarshal := json.Unmarshal(aggregated, &completion); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	choices, _ := completion["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if _, okCalls := message["tool_calls"]; okCalls {
		t.Fatal("truncated tool calls must be dropped")
	}
}

func TestAggregateEmptyStream(t *testing.T) {
	if _, errAggregate := aggregateChatCompletion(strings.NewReader("data: [DONE]\n\n")); errAggregate == nil {
		t.Fatal("empty stream must fail")
	}
}
