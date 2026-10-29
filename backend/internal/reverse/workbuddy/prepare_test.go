package workbuddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

func prepareForTest(t *testing.T, body string, opts prepareOptions) map[string]any {
	t.Helper()
	prepared := prepareBody([]byte(body), opts)
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(prepared, &payload); errUnmarshal != nil {
		t.Fatalf("decode prepared body: %v (raw=%s)", errUnmarshal, prepared)
	}
	return payload
}

func messagesOf(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, okMessages := payload["messages"].([]any)
	if !okMessages {
		t.Fatalf("messages missing: %v", payload["messages"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			t.Fatalf("message is not an object: %v", item)
		}
		out = append(out, message)
	}
	return out
}

// TestPrepareBodyCoreRules covers stream forcing, token alias translation,
// stream_options defaulting, developer flattening and image_url normalization.
func TestPrepareBodyCoreRules(t *testing.T) {
	payload := prepareForTest(t, `{
		"model":"glm-5.2",
		"stream":false,
		"max_completion_tokens":32000,
		"messages":[{"role":"developer","content":"be nice"},
		            {"role":"user","content":[{"type":"image_url","image_url":"http://x/y.png"}]}]
	}`, prepareOptions{})

	if payload["stream"] != true {
		t.Fatalf("stream must be forced to true, got %v", payload["stream"])
	}
	if _, okAlias := payload["max_completion_tokens"]; okAlias {
		t.Fatal("max_completion_tokens alias must be removed")
	}
	if payload["max_tokens"] != float64(32000) {
		t.Fatalf("max_tokens = %v, want 32000", payload["max_tokens"])
	}
	if _, okOptions := payload["stream_options"]; !okOptions {
		t.Fatal("stream_options must default to include_usage")
	}

	messages := messagesOf(t, payload)
	if messages[0]["role"] != "system" {
		t.Fatalf("developer must normalize to system, got %v", messages[0]["role"])
	}
	parts, _ := messages[1]["content"].([]any)
	part, _ := parts[0].(map[string]any)
	image, okImage := part["image_url"].(map[string]any)
	if !okImage || image["url"] != "http://x/y.png" {
		t.Fatalf("image_url = %v, want {url: ...}", part["image_url"])
	}
}

func TestPrepareBodyExplicitMaxTokensWins(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","max_tokens":100,"max_completion_tokens":200,"messages":[]}`, prepareOptions{})
	if payload["max_tokens"] != float64(100) {
		t.Fatalf("explicit max_tokens must win, got %v", payload["max_tokens"])
	}
	if _, okAlias := payload["max_completion_tokens"]; okAlias {
		t.Fatal("alias must still be removed")
	}
}

// TestPrepareBodyToolChoiceNone verifies "none" drops both the selector and the
// tool definitions (the upstream rejects object forms and unused tools).
func TestPrepareBodyToolChoiceNone(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","tool_choice":"none","tools":[{"type":"function"}],"functions":[{"name":"x"}],"messages":[]}`, prepareOptions{})
	for _, key := range []string{"tool_choice", "tools", "functions"} {
		if _, okKey := payload[key]; okKey {
			t.Fatalf("%s must be dropped for tool_choice=none", key)
		}
	}
}

func TestPrepareBodyToolChoiceObject(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","tool_choice":{"type":"function","function":{"name":"Bash"}},"messages":[]}`, prepareOptions{})
	if payload["tool_choice"] != "Bash" {
		t.Fatalf("tool_choice = %v, want Bash", payload["tool_choice"])
	}
	auto := prepareForTest(t, `{"model":"m","tool_choice":{"type":"auto"},"messages":[]}`, prepareOptions{})
	if auto["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %v, want auto", auto["tool_choice"])
	}
}

// TestPrepareBodyPromptCustom verifies custom mode replaces every system and
// developer message with one system block.
func TestPrepareBodyPromptCustom(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","messages":[
		{"role":"system","content":"old system"},
		{"role":"developer","content":"old developer"},
		{"role":"user","content":"hello"}
	]}`, prepareOptions{promptMode: "custom", promptText: "NEW PROMPT"})

	messages := messagesOf(t, payload)
	if len(messages) != 2 {
		t.Fatalf("custom mode must keep system + user only, got %d messages", len(messages))
	}
	if messages[0]["role"] != "system" || messages[0]["content"] != "NEW PROMPT" {
		t.Fatalf("first message = %v", messages[0])
	}
	if messages[1]["role"] != "user" || messages[1]["content"] != "hello" {
		t.Fatalf("second message = %v", messages[1])
	}
}

// TestPrepareBodyPromptAppend verifies append mode inserts after the leading
// system blocks.
func TestPrepareBodyPromptAppend(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","messages":[
		{"role":"system","content":"s1"},
		{"role":"system","content":"s2"},
		{"role":"user","content":"hello"}
	]}`, prepareOptions{promptMode: "append", promptText: "EXTRA"})

	messages := messagesOf(t, payload)
	if len(messages) != 4 {
		t.Fatalf("append mode must add one message, got %d", len(messages))
	}
	if messages[2]["content"] != "EXTRA" {
		t.Fatalf("append must insert after leading system blocks, got %v", messages[2])
	}
	if messages[3]["role"] != "user" {
		t.Fatalf("user message moved: %v", messages[3])
	}
}

// TestPromptSettingsDefaultText verifies the embedded prompt is used when a
// custom/append mode has no text.
func TestPromptSettingsDefaultText(t *testing.T) {
	acc := &reverse.Account{Extra: map[string]any{"reverse_prompt_mode": "append"}}
	mode, text := promptSettings(acc)
	if mode != "append" {
		t.Fatalf("mode = %q", mode)
	}
	if !strings.Contains(text, "工程助手") {
		t.Fatalf("default prompt not applied: %.40q", text)
	}

	passthrough, _ := promptSettings(&reverse.Account{Extra: map[string]any{"reverse_prompt_mode": "passthrough"}})
	if passthrough != "passthrough" {
		t.Fatalf("mode = %q", passthrough)
	}
	custom, customText := promptSettings(&reverse.Account{Extra: map[string]any{
		"reverse_prompt_mode": "custom",
		"reverse_prompt_text": "  MINE  ",
	}})
	if custom != "custom" || customText != "MINE" {
		t.Fatalf("custom prompt = %q / %q", custom, customText)
	}
}

// TestPrepareBodyOrphanToolCalls verifies symmetric trimming: an assistant
// tool_call without a result and a tool result without a call are both removed,
// while a complete pair is preserved.
func TestPrepareBodyOrphanToolCalls(t *testing.T) {
	orphan := prepareForTest(t, `{"model":"m","messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"A","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c2","content":"orphan"}
	]}`, prepareOptions{})
	messages := messagesOf(t, orphan)
	if len(messages) != 1 {
		t.Fatalf("orphan tool result must be removed, got %d messages", len(messages))
	}
	if _, okCalls := messages[0]["tool_calls"]; okCalls {
		t.Fatal("unpaired tool_calls must be dropped")
	}

	paired := prepareForTest(t, `{"model":"m","messages":[
		{"role":"assistant","tool_calls":[{"id":"c1","function":{"name":"A","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"ok"}
	]}`, prepareOptions{})
	pairedMessages := messagesOf(t, paired)
	if len(pairedMessages) != 2 {
		t.Fatalf("complete pair must be preserved, got %d messages", len(pairedMessages))
	}
	if _, okCalls := pairedMessages[0]["tool_calls"]; !okCalls {
		t.Fatal("paired tool_calls must be preserved")
	}
}

// TestPrepareBodyDeepSeekThinking verifies deepseek thinking injection and
// reasoning backfill (some tenants reject assistant messages without it).
func TestPrepareBodyDeepSeekThinking(t *testing.T) {
	payload := prepareForTest(t, `{"model":"deepseek-v4.1-flash","messages":[{"role":"assistant","content":"hi"}]}`, prepareOptions{})
	thinking, okThinking := payload["thinking"].(map[string]any)
	if !okThinking || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v", payload["thinking"])
	}
	if payload["reasoning_effort"] != defaultDeepSeekEffort {
		t.Fatalf("reasoning_effort = %v", payload["reasoning_effort"])
	}
	messages := messagesOf(t, payload)
	if reasoning, _ := messages[0]["reasoning"].(string); reasoning == "" {
		t.Fatal("assistant reasoning must be non-empty")
	}
	if _, okContent := messages[0]["reasoning_content"]; !okContent {
		t.Fatal("assistant reasoning_content must be backfilled")
	}

	disabled := prepareForTest(t, `{"model":"deepseek-v4.1-flash","thinking":{"type":"disabled"},"reasoning_effort":"high","messages":[]}`, prepareOptions{})
	if _, okEffort := disabled["reasoning_effort"]; okEffort {
		t.Fatal("disabled thinking must drop reasoning_effort")
	}

	other := prepareForTest(t, `{"model":"glm-5.2","messages":[]}`, prepareOptions{})
	if _, okThinking := other["thinking"]; okThinking {
		t.Fatal("non-deepseek models must not get thinking injected")
	}
}

// TestPrepareBodyEffortDowngrade verifies unsupported efforts are downgraded.
func TestPrepareBodyEffortDowngrade(t *testing.T) {
	payload := prepareForTest(t, `{"model":"deepseek-v4.1-flash","reasoning_effort":"xhigh","messages":[]}`, prepareOptions{
		supportedEfforts: map[string][]string{"deepseek-v4.1-flash": {"low", "high", "max"}},
	})
	if payload["reasoning_effort"] != "high" {
		t.Fatalf("xhigh should downgrade to high, got %v", payload["reasoning_effort"])
	}
}

// TestPrepareBodyGlobalSystemFallback verifies the global system fallback.
func TestPrepareBodyGlobalSystemFallback(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, prepareOptions{global: true})
	messages := messagesOf(t, payload)
	if len(messages) != 2 || messages[0]["role"] != "system" {
		t.Fatalf("global must prepend a system message, got %v", messages)
	}
}

// TestPrepareBodyCacheKey verifies prompt_cache_key injection.
func TestPrepareBodyCacheKey(t *testing.T) {
	payload := prepareForTest(t, `{"model":"m","messages":[]}`, prepareOptions{cacheKey: "wb2a-test"})
	if payload["prompt_cache_key"] != "wb2a-test" {
		t.Fatalf("prompt_cache_key = %v", payload["prompt_cache_key"])
	}
	explicit := prepareForTest(t, `{"model":"m","prompt_cache_key":"client","messages":[]}`, prepareOptions{cacheKey: "wb2a-test"})
	if explicit["prompt_cache_key"] != "client" {
		t.Fatal("client-provided prompt_cache_key must win")
	}
}
