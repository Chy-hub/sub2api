package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

func TestCustomBase64RoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		[]byte("a"),
		[]byte("ab"),
		[]byte("abc"),
		[]byte("hello, qoder"),
		[]byte("Qoder 中文测试 🚀"),
	}
	all := make([]byte, 0, 64)
	for i := 0; i < 64; i++ {
		all = append(all, byte(i))
	}
	cases = append(cases, all)

	for _, plain := range cases {
		encoded, err := encodeCustomBase64(plain)
		if err != nil {
			t.Fatalf("encode %q: %v", plain, err)
		}
		decoded, err := decodeCustomBase64(encoded)
		if err != nil {
			t.Fatalf("decode %q: %v", encoded, err)
		}
		if !bytes.Equal(decoded, plain) {
			t.Fatalf("round trip mismatch: got %q want %q", decoded, plain)
		}
	}

	// Locked vector computed from the reference algorithm: base64("hello") is
	// "aGVsbG8=", re-blocked as "8=VsbGaG" and mapped through the custom
	// alphabet.
	if encoded, err := encodeCustomBase64([]byte("hello")); err != nil || encoded != "q$FruHPH" {
		t.Fatalf("encodeCustomBase64(hello) = %q, %v", encoded, err)
	}
	if decoded, err := decodeCustomBase64("q$FruHPH"); err != nil || string(decoded) != "hello" {
		t.Fatalf("decodeCustomBase64(q$FruHPH) = %q, %v", decoded, err)
	}

	if _, err := decodeCustomBase64("not-base64!"); err == nil {
		t.Fatal("expected invalid alphabet to be rejected")
	}
}

func TestSignDeterminism(t *testing.T) {
	first := signRequest("payload", "cosy-key", "1700000000", `{"a":1}`, "/api/v2/x")
	second := signRequest("payload", "cosy-key", "1700000000", `{"a":1}`, "/api/v2/x")
	if first != second {
		t.Fatalf("signature is not deterministic: %q vs %q", first, second)
	}
	if len(first) != 32 {
		t.Fatalf("signature length = %d, want 32 hex chars", len(first))
	}
	if want := "796621a3461e535879f013d03f92da68"; first != want {
		t.Fatalf("signature = %q, want %q", first, want)
	}
	if other := signRequest("payload", "cosy-key", "1700000000", `{"a":2}`, "/api/v2/x"); other == first {
		t.Fatal("signature must change with the body")
	}
	if other := signRequest("payload", "cosy-key", "1700000000", `{"a":1}`, "/api/v2/y"); other == first {
		t.Fatal("signature must change with the path")
	}
	if signLegacy("Mon, 02 Jan 2006 15:04:05 GMT") != signLegacy("Mon, 02 Jan 2006 15:04:05 GMT") {
		t.Fatal("legacy signature is not deterministic")
	}
	if signLegacy("date-a") == signLegacy("date-b") {
		t.Fatal("legacy signature must change with the date")
	}
	if want := "93ce95b069c6e7f6b7caec9120453615"; signLegacy("Mon, 02 Jan 2006 15:04:05 GMT") != want {
		t.Fatalf("legacy signature = %q, want %q", signLegacy("Mon, 02 Jan 2006 15:04:05 GMT"), want)
	}

	path, err := pathSignature("https://api1.qoder.sh/algo/api/v2/model/list?Encode=1")
	if err != nil || path != "/api/v2/model/list" {
		t.Fatalf("pathSignature = %q, %v", path, err)
	}
}

func TestExtractDelta(t *testing.T) {
	content := extractDelta(wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"hi"}}]}`))
	if content.Role != "assistant" || content.Content != "hi" || content.Err != nil {
		t.Fatalf("content delta = %+v", content)
	}

	reasoning := extractDelta(wrapEnvelope(t, `{"choices":[{"delta":{"reasoning_content":"think"}}]}`))
	if reasoning.Reasoning != "think" {
		t.Fatalf("reasoning delta = %+v", reasoning)
	}

	toolDelta := extractDelta(wrapEnvelope(t, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Bash","arguments":"{}"}}]}}]}`))
	if len(toolDelta.ToolCalls) != 1 {
		t.Fatalf("tool delta = %+v", toolDelta)
	}

	usage := extractDelta(wrapEnvelope(t, `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`))
	if usage.FinishReason != "stop" || usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.TotalTokens != 14 {
		t.Fatalf("usage delta = %+v", usage)
	}

	envelopeErr := extractDelta(mustJSON(t, map[string]any{"statusCodeValue": 418, "body": "provider_error"}))
	if envelopeErr.Err == nil || envelopeErr.Err.StatusCode != 418 {
		t.Fatalf("envelope error = %+v", envelopeErr)
	}

	stringStatus := extractDelta(mustJSON(t, map[string]any{"statusCodeValue": "503", "body": "busy"}))
	if stringStatus.Err == nil || stringStatus.Err.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("string envelope status = %+v", stringStatus)
	}

	businessErr := extractDelta(wrapEnvelope(t, `{"code":"115","message":"model busy"}`))
	if businessErr.Err == nil || businessErr.Err.Code != "115" {
		t.Fatalf("business error = %+v", businessErr)
	}

	if empty := extractDelta("not json"); !empty.empty() {
		t.Fatalf("garbage payload should be ignored, got %+v", empty)
	}
}

func TestConsumeStreamAggregatesContentUsageAndToolCalls(t *testing.T) {
	stream := strings.Join([]string{
		"data: " + wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"Hello "}}]}`),
		"",
		"data: " + wrapEnvelope(t, `{"choices":[{"delta":{"content":"world","tool_calls":[{"index":0,"id":"call_1","function":{"name":"Bash","arguments":"{\"command\":"}}]}}]}`),
		"",
		"data: " + wrapEnvelope(t, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`),
		"",
		"data: [DONE]",
		"",
	}, "\n")

	state, err := consumeStream(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("consumeStream: %v", err)
	}
	if state.content.String() != "Hello world" {
		t.Fatalf("content = %q", state.content.String())
	}
	if state.inputTokens != 10 || state.outputTokens != 2 || state.totalTokens != 12 {
		t.Fatalf("usage = %d/%d/%d", state.inputTokens, state.outputTokens, state.totalTokens)
	}
	calls := filterValidToolCalls(state.toolCalls)
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v", calls)
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if function["name"] != "Bash" || function["arguments"] != `{"command":"ls"}` {
		t.Fatalf("merged tool call = %+v", call)
	}
}

func TestPumpStreamEmitsValidSSE(t *testing.T) {
	stream := strings.Join([]string{
		"data: " + wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"hi"}}]}`),
		"",
		"data: " + wrapEnvelope(t, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"Bash","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`),
		"",
		"data: [DONE]",
		"",
	}, "\n")

	var output bytes.Buffer
	if err := pumpStream(context.Background(), io.NopCloser(strings.NewReader(stream)), &output, "chatcmpl-test", 1, "dmodel", true); err != nil {
		t.Fatalf("pumpStream: %v", err)
	}
	raw := output.String()
	if !strings.HasSuffix(raw, "data: [DONE]\n\n") {
		t.Fatalf("stream must end with [DONE], got %q", raw)
	}
	chunks := 0
	sawFinish := false
	sawUsage := false
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		payload := strings.TrimPrefix(block, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE chunk %q: %v", payload, err)
		}
		chunks++
		if chunk["object"] != "chat.completion.chunk" {
			t.Fatalf("object = %v", chunk["object"])
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			if usage, ok := chunk["usage"].(map[string]any); ok && intValue(usage["prompt_tokens"]) == 3 {
				sawUsage = true
			}
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if choice["finish_reason"] == "tool_calls" {
			sawFinish = true
		}
	}
	if chunks < 3 || !sawFinish || !sawUsage {
		t.Fatalf("chunks=%d finish=%v usage=%v\n%s", chunks, sawFinish, sawUsage, raw)
	}
}

func TestBuildEnvelopeDoesNotLeakTemplateTools(t *testing.T) {
	template, err := templateBase()
	if err != nil {
		t.Fatalf("templateBase: %v", err)
	}

	request, err := decodeChatRequest([]byte(`{"model":"dmodel","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("decodeChatRequest: %v", err)
	}
	envelope := buildEnvelope(template, request, "dmodel", "personal_standard")
	tools, ok := envelope["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Fatalf("template tools leaked into a request without tools: %#v", envelope["tools"])
	}
	if templateTools, _ := template["tools"].([]any); len(templateTools) == 0 {
		t.Fatal("fixture template unexpectedly has no tools")
	}
	if messages, _ := envelope["messages"].([]any); len(messages) != 1 {
		t.Fatalf("only the client user message is expected, got %#v", envelope["messages"])
	}

	withTools := `{"model":"dmodel","messages":[{"role":"user","content":"hello"}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
	request, err = decodeChatRequest([]byte(withTools))
	if err != nil {
		t.Fatalf("decodeChatRequest: %v", err)
	}
	envelope = buildEnvelope(template, request, "dmodel", "personal_standard")
	tools, _ = envelope["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("client tools must be forwarded, got %#v", envelope["tools"])
	}

	request, err = decodeChatRequest([]byte(`{"model":"dmodel","messages":[{"role":"user","content":"hello"}],"tools":[]}`))
	if err != nil {
		t.Fatalf("decodeChatRequest: %v", err)
	}
	envelope = buildEnvelope(template, request, "dmodel", "personal_standard")
	if tools, _ := envelope["tools"].([]any); len(tools) != 0 {
		t.Fatalf("explicit empty tools must not leak the template: %#v", envelope["tools"])
	}
}

func TestDecodeJSONResponseCustomBase64(t *testing.T) {
	plain := []byte(`{"assistant":[{"key":"dmodel","display_name":"DeepSeek-V4-Pro","enable":true}]}`)
	encoded, err := encodeCustomBase64(plain)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	parsed, err := decodeJSONResponse([]byte(encoded))
	if err != nil {
		t.Fatalf("decodeJSONResponse: %v", err)
	}
	models, resolution := parseModels(parsed)
	if len(models) != 1 || models[0].ID != "deepseek-v4-pro" || models[0].DisplayName != "DeepSeek-V4-Pro" {
		t.Fatalf("models = %#v", models)
	}
	if resolution["deepseek-v4-pro"] != "dmodel" {
		t.Fatalf("resolution = %#v", resolution)
	}

	plainParsed, err := decodeJSONResponse(plain)
	if err != nil || len(plainParsed) == 0 {
		t.Fatalf("plain JSON should decode: %v", err)
	}
}

func TestBuildEnvelopeDropsAssistantToolCallsWithoutClientTools(t *testing.T) {
	template, err := templateBase()
	if err != nil {
		t.Fatalf("templateBase: %v", err)
	}
	payload := `{"model":"dmodel","messages":[
		{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"Bash","arguments":"{}"}}]},
		{"role":"user","content":"next"}]}`
	request, err := decodeChatRequest([]byte(payload))
	if err != nil {
		t.Fatalf("decodeChatRequest: %v", err)
	}
	envelope := buildEnvelope(template, request, "dmodel", "personal_standard")
	messages, _ := envelope["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if stringValue(message["role"]) == "assistant" {
			t.Fatalf("orphan assistant tool call must be dropped: %#v", messages)
		}
	}
}

func TestTokenTypeDetection(t *testing.T) {
	cases := []struct {
		token string
		want  tokenType
	}{
		{"dt-abc", tokenDevice},
		{"pt-abc", tokenPersonal},
		{"drt-abc", tokenRefresh},
		{"", tokenUnknown},
		{"abc", tokenUnknown},
	}
	for _, testCase := range cases {
		if got := detectTokenType(testCase.token); got != testCase.want {
			t.Errorf("detectTokenType(%q) = %v, want %v", testCase.token, got, testCase.want)
		}
	}
}

func TestMachineDerivationIsStable(t *testing.T) {
	seed := "account-seed"
	id := deriveMachineID(seed)
	if len(id) != 32 || id != deriveMachineID(seed) {
		t.Fatalf("machine id = %q", id)
	}
	if other := deriveMachineID("other-seed"); other == id {
		t.Fatal("different seeds must yield different machine ids")
	}
	if machineType := deriveMachineType(seed); len(machineType) != 18 {
		t.Fatalf("machine type = %q", machineType)
	}
	if token := deriveMachineToken(seed); len(token) != 88 {
		t.Fatalf("machine token length = %d", len(token))
	}
}

func TestCheckinIdentityUsesOnlyUserSuppliedMachinePair(t *testing.T) {
	account := &reverse.Account{Credentials: map[string]any{}}
	identity := checkinIdentityFromCredentials(account)
	if identity.native() || identity.Token != "" || identity.Type != "" {
		t.Fatalf("check-in must never derive a machine identity: %+v", identity)
	}

	account.Credentials["machine_token"] = "machine-token"
	if half := checkinIdentityFromCredentials(account); half.native() {
		t.Fatalf("a half machine identity must stay unusable: %+v", half)
	}
	account.Credentials["machine_type"] = "machine-type"
	account.Credentials["machine_code"] = "machine-code"
	identity = checkinIdentityFromCredentials(account)
	if !identity.native() || identity.Token != "machine-token" || identity.Type != "machine-type" || identity.Code != "machine-code" {
		t.Fatalf("user-supplied identity must be passed through: %+v", identity)
	}

	message := noCampaignMessage(regionGlobal, checkinIdentity{})
	if !strings.Contains(message, "machine_token") || !strings.Contains(message, "machine_type") {
		t.Fatalf("international no-campaign message must explain the machine identity requirement: %q", message)
	}
}

func TestCampaignWindowSemantics(t *testing.T) {
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, 10, day, hour, minute, 0, 0, campaignZone)
	}
	previousRound := &campaign{
		StartAt: at(25, 10, 0).Unix(),
		EndAt:   at(26, 10, 0).Unix(),
	}
	morning := at(26, 9, 0)
	if !campaignWindowCovers(previousRound, morning) {
		t.Fatal("yesterday's round still covers 09:00 today")
	}
	if isTodayRoundWindow(previousRound, morning) {
		t.Fatal("yesterday's round must not count as today's round")
	}
	if date := campaignWindowDate(previousRound); date != "2026-10-25" {
		t.Fatalf("window date = %q", date)
	}

	todayRound := &campaign{StartAt: at(26, 10, 0).Unix()}
	if !isTodayRoundWindow(todayRound, at(26, 10, 1)) {
		t.Fatal("10:01 belongs to today's round")
	}
	if campaignWindowCovers(todayRound, morning) {
		t.Fatal("today's 10:00 round must not cover 09:00")
	}
}

func TestUpstreamErrorRedactsSecrets(t *testing.T) {
	err := upstreamError(http.StatusUnauthorized, nil,
		[]byte(`{"message":"token dt-supersecrettoken rejected"}`),
		"dt-supersecrettoken")
	if strings.Contains(err.Message, "dt-supersecrettoken") {
		t.Fatalf("error leaked the token: %q", err.Message)
	}
	if !strings.Contains(err.Message, "[redacted]") {
		t.Fatalf("expected redaction marker, got %q", err.Message)
	}
}

func TestForwardNonStreamEndToEnd(t *testing.T) {
	original := endpointsGlobal
	defer func() { endpointsGlobal = original }()

	type captured struct {
		authorization string
		machineType   string
		envelope      map[string]any
		decodeErr     error
	}
	contentFrame := wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`)
	usageFrame := wrapEnvelope(t, `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
	requests := make(chan captured, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/userinfo"):
			_, _ = io.WriteString(w, `{"id":"u1","name":"Tester","userType":"personal_standard"}`)
		case strings.HasSuffix(r.URL.Path, "/chat"):
			raw, _ := io.ReadAll(r.Body)
			decoded, err := decodeCustomBase64(string(raw))
			var envelope map[string]any
			if err == nil {
				err = json.Unmarshal(decoded, &envelope)
			}
			requests <- captured{
				authorization: r.Header.Get("authorization"),
				machineType:   r.Header.Get("cosy-machinetype"),
				envelope:      envelope,
				decodeErr:     err,
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+contentFrame+"\n\n")
			_, _ = io.WriteString(w, "data: "+usageFrame+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpointsGlobal.userinfo = server.URL + "/api/v1/userinfo"
	endpointsGlobal.chat = server.URL + "/algo/api/v2/chat"

	provider := New().(*Provider)
	account := &reverse.Account{
		ID:       1,
		Region:   "global",
		Platform: "qoder_global",
		Credentials: map[string]any{
			"access_token": "dt-account-token-123456",
			"uid":          "u1",
		},
	}
	body := []byte(`{"model":"dmodel","messages":[{"role":"user","content":"hello"}]}`)
	response, err := provider.Forward(context.Background(), account, body, false)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &completion); err != nil {
		t.Fatalf("decode response %q: %v", raw, err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "Hello" {
		t.Fatalf("completion = %+v", completion)
	}
	if completion.Choices[0].FinishReason != "stop" || completion.Usage.PromptTokens != 5 {
		t.Fatalf("completion usage/finish = %+v", completion)
	}

	capturedRequest := <-requests
	if capturedRequest.decodeErr != nil {
		t.Fatalf("decode forwarded envelope: %v", capturedRequest.decodeErr)
	}
	if !strings.HasPrefix(capturedRequest.authorization, "Bearer COSY.") {
		t.Fatalf("authorization = %q", capturedRequest.authorization)
	}
	if capturedRequest.machineType == "" {
		t.Fatal("machine type header missing")
	}
	if capturedRequest.envelope["stream"] != true {
		t.Fatalf("envelope stream = %v", capturedRequest.envelope["stream"])
	}
	modelConfig, _ := capturedRequest.envelope["model_config"].(map[string]any)
	if modelConfig["key"] != "dmodel" {
		t.Fatalf("model key = %v", modelConfig["key"])
	}
	tools, _ := capturedRequest.envelope["tools"].([]any)
	if len(tools) != 0 {
		t.Fatalf("forwarded envelope leaked template tools: %#v", tools)
	}
	messages, _ := capturedRequest.envelope["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("forwarded messages = %#v", messages)
	}
}

func TestBuildEnvelopeNeverInjectsDesktopPrompt(t *testing.T) {
	template, err := templateBase()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		messages string
		roles    []string
		texts    []string
	}{
		{"user only", `[{"role":"user","content":"hello"}]`, []string{"user"}, []string{"hello"}},
		{"system", `[{"role":"system","content":"You are my agent."},{"role":"user","content":"hello"}]`, []string{"system", "user"}, []string{"You are my agent.", "hello"}},
		{"developer", `[{"role":"developer","content":"Follow my agent's instructions."},{"role":"user","content":"hello"}]`, []string{"developer", "user"}, []string{"Follow my agent's instructions.", "hello"}},
		{"empty system", `[{"role":"system","content":""},{"role":"user","content":"hello"}]`, []string{"user"}, []string{"hello"}},
		{"empty messages", `[]`, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := decodeChatRequest([]byte(`{"model":"dmodel","messages":` + test.messages + `}`))
			if err != nil {
				t.Fatal(err)
			}
			envelope := buildEnvelope(template, request, "dmodel", "personal_standard")
			messages := envelope["messages"].([]any)
			if len(messages) != len(test.roles) {
				t.Fatalf("unexpected message count: %#v", messages)
			}
			for i, raw := range messages {
				message := raw.(map[string]any)
				text := normalizeMessageContent(message)
				if test.roles[i] == "user" {
					parts, _ := message["contents"].([]any)
					text = rawStringValue(parts[0].(map[string]any)["text"])
				}
				if stringValue(message["role"]) != test.roles[i] || text != test.texts[i] {
					t.Fatalf("client message changed: %#v", message)
				}
			}
			encoded := mustJSON(t, envelope)
			for _, fingerprint := range []string{"You are Qoder", "D:/Projects/Qoder", "Platform: windows", "create-subagent"} {
				if strings.Contains(encoded, fingerprint) {
					t.Fatalf("desktop template instruction leaked: %s", fingerprint)
				}
			}
			if envelope["session_type"] != "qoder" || envelope["chat_task"] != "FREE_INPUT" || envelope["stream"] != true {
				t.Fatal("protocol fields must remain available without a default prompt")
			}
		})
	}
}

func wrapEnvelope(t *testing.T, inner string) string {
	t.Helper()
	return mustJSON(t, map[string]any{"headers": map[string]any{}, "body": inner, "statusCodeValue": 200})
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(encoded)
}
