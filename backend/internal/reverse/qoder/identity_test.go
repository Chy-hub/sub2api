package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/Wei-Shaw/sub2api/internal/reverse/textsanitize"
)

// 本文件锁定「出站身份」三件事：会话标识的连续性、UA 覆盖、以及按账号开关的
// 文本指纹净化。三者都不改变上游协议，只改变出站字段的取值。

var uuidShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// uuidAny 匹配任意 UUID 形状（不校验版本位）。每请求随机的 session_id 走的是
// newUUID()，它不带版本位，这里只要求形状成立。
var uuidAny = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestConversationKeyPriority(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"metadata", `{"metadata":{"conversation_id":"conv-meta"},"conversation_id":"conv-top","prompt_cache_key":"cache"}`, "conv-meta"},
		{"metadata camel", `{"metadata":{"conversationId":"conv-camel"}}`, "conv-camel"},
		{"top level", `{"conversation_id":"conv-top","prompt_cache_key":"cache"}`, "conv-top"},
		{"prompt cache", `{"prompt_cache_key":"cache"}`, "cache"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(test.body), &payload); err != nil {
				t.Fatal(err)
			}
			if got := conversationKey(payload); got != test.want {
				t.Fatalf("conversationKey = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDerivedConversationKeyIsStableAndTextSensitive locks the fallback's two
// properties: the same conversation yields the same key, and a different system
// prompt yields a different one (the documented cost of deriving it).
func TestDerivedConversationKeyIsStableAndTextSensitive(t *testing.T) {
	body := func(system string) map[string]any {
		return map[string]any{
			"messages": []any{
				map[string]any{"role": "system", "content": system},
				map[string]any{"role": "user", "content": "hello"},
			},
		}
	}
	first := conversationKey(body("you are helpful"))
	if first == "" {
		t.Fatal("derived key must not be empty for a normal conversation")
	}
	if second := conversationKey(body("you are helpful")); second != first {
		t.Fatalf("derived key is not stable: %q vs %q", first, second)
	}
	if other := conversationKey(body("you are a different agent")); other == first {
		t.Fatal("changing the system prompt must change the derived key")
	}

	empty := map[string]any{"messages": []any{map[string]any{"role": "user", "content": ""}}}
	if key := conversationKey(empty); key != "" {
		t.Fatalf("no usable text must yield no key, got %q", key)
	}
}

func TestSessionUUIDShape(t *testing.T) {
	first := sessionUUID("conv-abc")
	if !uuidShape.MatchString(first) {
		t.Fatalf("session id is not a v5 UUID: %q", first)
	}
	if second := sessionUUID("conv-abc"); second != first {
		t.Fatalf("session id is not deterministic: %q vs %q", first, second)
	}
	if other := sessionUUID("conv-xyz"); other == first {
		t.Fatal("different conversations must get different session ids")
	}
	// 空键退回每请求随机：这是拿不到会话标识时的旧行为。
	if empty := sessionUUID(""); !uuidAny.MatchString(empty) {
		t.Fatalf("empty key must still produce a UUID shape, got %q", empty)
	}
}

func TestSanitizeFingerprintsDefaultsOn(t *testing.T) {
	if !sanitizeFingerprints(&reverse.Account{Extra: map[string]any{}}) {
		t.Fatal("sanitization must default on, like WorkBuddy")
	}
	if !sanitizeFingerprints(&reverse.Account{}) {
		t.Fatal("an account without extra must still default on")
	}
	if sanitizeFingerprints(&reverse.Account{Extra: map[string]any{"reverse_sanitize_fingerprints": false}}) {
		t.Fatal("an explicit false must disable sanitization")
	}
	if !sanitizeFingerprints(&reverse.Account{Extra: map[string]any{"reverse_sanitize_fingerprints": true}}) {
		t.Fatal("an explicit true must enable sanitization")
	}
}

func TestChatUserAgentOverride(t *testing.T) {
	if got := outboundUserAgent(&reverse.Account{}); got != defaultUserAgent {
		t.Fatalf("default UA = %q, want %q", got, defaultUserAgent)
	}
	account := &reverse.Account{Credentials: map[string]any{"user_agent": "Qoder/9.9.9"}}
	if got := outboundUserAgent(account); got != "Qoder/9.9.9" {
		t.Fatalf("override UA = %q", got)
	}
	// 空白覆盖不算覆盖，不能把 UA 变成空串。
	if got := outboundUserAgent(&reverse.Account{Credentials: map[string]any{"user_agent": "   "}}); got != defaultUserAgent {
		t.Fatalf("blank override must fall back to the default, got %q", got)
	}
	// 含控制字符的 UA 会让 http.Client.Do 直接失败：必须退回默认值，而不是让账号
	// 连 bridge 都建不起来。
	for _, bad := range []string{"Qoder\r\nX-Injected: 1", "Qoder\x00", strings.Repeat("a", maxUserAgentLen+1)} {
		if got := outboundUserAgent(&reverse.Account{Credentials: map[string]any{"user_agent": bad}}); got != defaultUserAgent {
			t.Fatalf("unusable override %q must fall back to the default, got %q", bad, got)
		}
	}
	// 只是带空格的值仍然可用，去掉首尾空白即可。
	if got := outboundUserAgent(&reverse.Account{Credentials: map[string]any{"user_agent": "  Qoder/9.9.9  "}}); got != "Qoder/9.9.9" {
		t.Fatalf("padded override = %q", got)
	}
}

// fakeChatUpstream 捕获对话请求的 envelope 与 UA，并回一个最小可用流。
func fakeChatUpstream(t *testing.T) (*httptest.Server, <-chan map[string]any, <-chan string) {
	t.Helper()
	return fakeChatUpstreamWithUID(t, nil)
}

// fakeChatUpstreamWithUID 同上，但 userinfo 的 uid 由 token 决定（nil 时固定 "u1"），
// 这样测试可以模拟多个账号。
func fakeChatUpstreamWithUID(t *testing.T, uidFor func(token string) string) (*httptest.Server, <-chan map[string]any, <-chan string) {
	t.Helper()
	envelopes := make(chan map[string]any, 8)
	userAgents := make(chan string, 8)
	contentFrame := wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"ok"}}]}`)
	usageFrame := wrapEnvelope(t, `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/userinfo"):
			uid := "u1"
			if uidFor != nil {
				uid = uidFor(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"userType":"personal_standard"}`, uid)
		case strings.HasSuffix(r.URL.Path, "/chat"):
			raw, _ := io.ReadAll(r.Body)
			decoded, errDecode := decodeCustomBase64(string(raw))
			var envelope map[string]any
			if errDecode == nil {
				errDecode = json.Unmarshal(decoded, &envelope)
			}
			if errDecode != nil {
				t.Errorf("decode forwarded envelope: %v", errDecode)
			}
			envelopes <- envelope
			userAgents <- r.Header.Get("user-agent")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+contentFrame+"\n\n")
			_, _ = io.WriteString(w, "data: "+usageFrame+"\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	return server, envelopes, userAgents
}

func installFakeEndpoints(t *testing.T, server *httptest.Server) {
	t.Helper()
	original := endpointsGlobal
	t.Cleanup(func() { endpointsGlobal = original })
	endpointsGlobal.userinfo = server.URL + "/api/v1/userinfo"
	endpointsGlobal.chat = server.URL + "/algo/api/v2/chat"
}

func newTestAccount(id int64, extra map[string]any, credentials map[string]any) *reverse.Account {
	creds := map[string]any{"access_token": "dt-account-token-123456", "uid": "u1"}
	for key, value := range credentials {
		creds[key] = value
	}
	return &reverse.Account{
		ID:          id,
		Region:      "global",
		Platform:    "qoder_global",
		Credentials: creds,
		Extra:       extra,
	}
}

// qoderMessageText 取出 qoder 侧消息的正文：user 消息放在 contents[] 里，
// system 消息放在 content 里。
func qoderMessageText(message map[string]any) string {
	if contents, okContents := message["contents"].([]any); okContents {
		var parts []string
		for _, item := range contents {
			part, okPart := item.(map[string]any)
			if !okPart {
				continue
			}
			if text := rawStringValue(part["text"]); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n\n")
		}
	}
	return rawStringValue(message["content"])
}

func forwardOnce(t *testing.T, provider *Provider, account *reverse.Account, body string) {
	t.Helper()
	response, err := provider.Forward(context.Background(), account, []byte(body), false)
	if err != nil {
		t.Fatalf("Forward: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
}

// TestForwardSanitizesFingerprintsPerAccount locks the switch: on by default,
// off per account, and the rewrite must reach every field derived from the
// prompt (messages, chat_context text, business.name).
func TestForwardSanitizesFingerprintsPerAccount(t *testing.T) {
	rules, _ := textsanitize.Load()
	if len(rules.Rewrites) == 0 || len(rules.Rewrites[0]) != 2 {
		t.Skip("sanitize rules are not embedded")
	}
	needle := rules.Rewrites[0][0]

	for _, test := range []struct {
		name        string
		extra       map[string]any
		wantRewrite bool
	}{
		{"default on", map[string]any{}, true},
		{"explicit on", map[string]any{"reverse_sanitize_fingerprints": true}, true},
		{"explicit off", map[string]any{"reverse_sanitize_fingerprints": false}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, envelopes, _ := fakeChatUpstream(t)
			defer server.Close()
			installFakeEndpoints(t, server)

			provider := New().(*Provider)
			account := newTestAccount(100, test.extra, nil)
			body, errMarshal := json.Marshal(map[string]any{
				"model":    "dmodel",
				"messages": []any{map[string]any{"role": "user", "content": needle}},
				// 工具定义原样透传：净化只覆盖消息体，这条缺口要锁住，别以为
				// 加了开关就全覆盖了。
				"tools": []any{map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        "Write",
						"description": needle,
						"parameters":  map[string]any{"type": "object"},
					},
				}},
			})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			forwardOnce(t, provider, account, string(body))
			envelope := <-envelopes

			messages, _ := envelope["messages"].([]any)
			if len(messages) != 1 {
				t.Fatalf("forwarded messages = %#v", messages)
			}
			text := qoderMessageText(messages[0].(map[string]any))

			context, _ := envelope["chat_context"].(map[string]any)
			textBlock, _ := context["text"].(map[string]any)
			contextText, _ := textBlock["text"].(string)

			// envelope 的第三条旁路：chat_context.extra.originalContent.text。
			extraBlock, _ := context["extra"].(map[string]any)
			originalBlock, _ := extraBlock["originalContent"].(map[string]any)
			originalText, _ := originalBlock["text"].(string)

			business, _ := envelope["business"].(map[string]any)
			businessName, _ := business["name"].(string)

			// 逐字段断言，而不是只看「至少一处被改」：漏掉任何一条旁路都会
			// 让同一句话从那里原样出去。originalContent 为空说明模板结构变了。
			if originalText == "" {
				t.Fatal("template must carry chat_context.extra.originalContent.text")
			}
			for name, value := range map[string]string{
				"messages":              text,
				"chat_context.text":     contextText,
				"chat_context.original": originalText,
			} {
				carries := strings.Contains(value, needle)
				if test.wantRewrite && carries {
					t.Fatalf("%s still carries the fingerprint: %q", name, value)
				}
				if !test.wantRewrite && !carries {
					t.Fatalf("%s lost the fingerprint although sanitization is off: %q", name, value)
				}
			}
			// business.name 是 prompt 的截断，必须跟着同一份（已净化的）文本走，
			// 否则同一句话会从这条旁路原样漏出去。
			if want := truncateRunes(text, 30); businessName != want {
				t.Fatalf("business.name must derive from the forwarded text: got %q want %q", businessName, want)
			}
			// 工具定义不在净化范围内（与 WorkBuddy 一致），这里锁住该行为，
			// 避免以后误以为净化覆盖了 tools。
			tools, _ := envelope["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("forwarded tools = %#v", tools)
			}
			toolFunction, _ := tools[0].(map[string]any)["function"].(map[string]any)
			if description, _ := toolFunction["description"].(string); description != needle {
				t.Fatalf("tool description must pass through untouched, got %q", description)
			}
		})
	}
}

// TestForwardKeepsSessionAcrossConversationTurns locks the continuity fix: two
// turns of one conversation share session_id while request_id stays per request.
func TestForwardKeepsSessionAcrossConversationTurns(t *testing.T) {
	server, envelopes, _ := fakeChatUpstream(t)
	defer server.Close()
	installFakeEndpoints(t, server)

	provider := New().(*Provider)
	account := newTestAccount(200, nil, nil)
	body := `{"model":"dmodel","metadata":{"conversation_id":"conv-abc"},"messages":[{"role":"user","content":"turn"}]}`

	forwardOnce(t, provider, account, body)
	first := <-envelopes
	forwardOnce(t, provider, account, body)
	second := <-envelopes

	if first["session_id"] != second["session_id"] {
		t.Fatalf("session_id must be stable across turns: %v vs %v", first["session_id"], second["session_id"])
	}
	if first["request_id"] == second["request_id"] {
		t.Fatal("request_id must stay per request")
	}
	if !uuidShape.MatchString(stringValue(first["session_id"])) {
		t.Fatalf("session_id is not a UUID: %v", first["session_id"])
	}

	// 拿不到会话标识（正文全空）时必须退回每请求随机，而不是把所有请求并进一个会话。
	empty := `{"model":"dmodel","messages":[{"role":"user","content":""}]}`
	forwardOnce(t, provider, account, empty)
	third := <-envelopes
	forwardOnce(t, provider, account, empty)
	fourth := <-envelopes
	if third["session_id"] == fourth["session_id"] {
		t.Fatal("without a conversation key each request must get its own session")
	}
}

func TestForwardSendsOverriddenUserAgent(t *testing.T) {
	server, _, userAgents := fakeChatUpstream(t)
	defer server.Close()
	installFakeEndpoints(t, server)

	provider := New().(*Provider)
	account := newTestAccount(300, nil, map[string]any{"user_agent": "Qoder/9.9.9"})
	forwardOnce(t, provider, account, `{"model":"dmodel","messages":[{"role":"user","content":"hi"}]}`)
	if got := <-userAgents; got != "Qoder/9.9.9" {
		t.Fatalf("outbound user-agent = %q", got)
	}

	plain := newTestAccount(301, nil, nil)
	forwardOnce(t, provider, plain, `{"model":"dmodel","messages":[{"role":"user","content":"hi"}]}`)
	if got := <-userAgents; got != defaultUserAgent {
		t.Fatalf("default outbound user-agent = %q", got)
	}
}

// TestSignedGetSendsOutboundUserAgent locks that the cosy-signed GET family
// (model catalogue, quota) reports the same client as chat: an override that
// only reaches one of the two is itself an inconsistency.
func TestSignedGetSendsOutboundUserAgent(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("user-agent"))
		_, _ = io.WriteString(w, `{"assistant":[]}`)
	}))
	defer server.Close()

	session, errSession := newSession(authIdentity{Uid: "u1"}, "machine-id", "machine-token", "machine-type")
	if errSession != nil {
		t.Fatalf("newSession: %v", errSession)
	}
	bridge := &bridge{session: session}
	if _, err := bridge.signedGet(context.Background(), server.Client(), server.URL+"/model/list", "Qoder/9.9.9"); err != nil {
		t.Fatalf("signedGet override: %v", err)
	}
	if _, err := bridge.signedGet(context.Background(), server.Client(), server.URL+"/model/list", ""); err != nil {
		t.Fatalf("signedGet default: %v", err)
	}
	if len(seen) != 2 || seen[0] != "Qoder/9.9.9" || seen[1] != defaultUserAgent {
		t.Fatalf("model listing user-agents = %#v", seen)
	}
}

// TestAccountSessionUUIDIsolatesAccounts locks the account dimension: the same
// conversation id served by two accounts must not collapse into one session,
// which is the shape an upstream uses to spot a shared account pool.
func TestAccountSessionUUIDIsolatesAccounts(t *testing.T) {
	first := accountSessionUUID(1, "u1", "conv-abc")
	second := accountSessionUUID(2, "u2", "conv-abc")
	if first == second {
		t.Fatalf("two accounts share one session id: %q", first)
	}
	if !uuidShape.MatchString(first) {
		t.Fatalf("session id is not a v5 UUID: %q", first)
	}
	if accountSessionUUID(1, "u1", "conv-abc") != first {
		t.Fatal("account-scoped session id is not deterministic")
	}
	// 没有会话键时每请求随机。
	if accountSessionUUID(1, "u1", "") == accountSessionUUID(1, "u1", "") {
		t.Fatal("without a conversation key each request must get its own session")
	}
	// uid 缺失（手工导入凭据 + 上游不回 id）时仍要按账号隔离。
	if accountSessionUUID(1, "", "conv-abc") == accountSessionUUID(2, "", "conv-abc") {
		t.Fatal("accounts without a uid must still get separate sessions")
	}
}

// TestCheckinHeadersUseOverriddenUserAgent locks that the activity endpoint
// reports the same client as the API calls when an account overrides the UA.
func TestCheckinHeadersUseOverriddenUserAgent(t *testing.T) {
	overridden := checkinHeaders("token", checkinIdentityFromCredentials(&reverse.Account{
		Credentials: map[string]any{"user_agent": "Qoder/9.9.9"},
	}))
	if overridden["user-agent"] != "Qoder/9.9.9" {
		t.Fatalf("checkin user-agent = %q", overridden["user-agent"])
	}
	plain := checkinHeaders("token", checkinIdentityFromCredentials(&reverse.Account{}))
	if plain["user-agent"] != "Qoder" {
		t.Fatalf("default checkin user-agent = %q", plain["user-agent"])
	}
}

// TestQuotaSendsOutboundUserAgent covers the one account-scoped call that does
// not go through the cosy header matrix.
func TestQuotaSendsOutboundUserAgent(t *testing.T) {
	server, _, _ := fakeChatUpstream(t)
	defer server.Close()
	installFakeEndpoints(t, server)

	type seenRequest struct {
		path      string
		userAgent string
	}
	var seen []seenRequest
	quotaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, seenRequest{path: r.URL.Path, userAgent: r.Header.Get("user-agent")})
		switch {
		case strings.HasSuffix(r.URL.Path, "/plan"):
			_, _ = io.WriteString(w, `{"plan_tier_name":"pro"}`)
		default:
			_, _ = io.WriteString(w, `{"userQuota":{"used":1,"total":100}}`)
		}
	}))
	defer quotaServer.Close()

	original := endpointsGlobal
	t.Cleanup(func() { endpointsGlobal = original })
	endpointsGlobal.quota = quotaServer.URL + "/quota"
	endpointsGlobal.plan = quotaServer.URL + "/plan"

	provider := New().(*Provider)
	account := newTestAccount(400, nil, map[string]any{"user_agent": "Qoder/9.9.9"})
	if _, err := provider.Quota(context.Background(), account); err != nil {
		t.Fatalf("Quota: %v", err)
	}
	// 额度查询固定打两条：quota 本体与套餐名。两条都必须带覆盖值，
	// 只断言「至少一条」会漏掉其中一条丢失 UA 的回归。
	if len(seen) != 2 {
		t.Fatalf("quota requests = %d (%#v)", len(seen), seen)
	}
	for _, request := range seen {
		if request.userAgent != "Qoder/9.9.9" {
			t.Fatalf("%s user-agent = %q", request.path, request.userAgent)
		}
	}
	if !strings.HasSuffix(seen[0].path, "/quota") || !strings.HasSuffix(seen[1].path, "/plan") {
		t.Fatalf("unexpected quota request order: %#v", seen)
	}
}

// TestForwardIsolatesSessionsAcrossAccounts locks the wiring, not just the
// helper: two accounts serving the same conversation id must emit different
// session ids. A helper-only test would still pass if Forward passed an empty
// uid.
func TestForwardIsolatesSessionsAcrossAccounts(t *testing.T) {
	server, envelopes, _ := fakeChatUpstreamWithUID(t, func(token string) string {
		if strings.Contains(token, "second") {
			return "u2"
		}
		return "u1"
	})
	defer server.Close()
	installFakeEndpoints(t, server)

	provider := New().(*Provider)
	// 两个账号都不带 uid 凭据：uid 只能来自 userinfo，这样同时覆盖
	// 「凭据里没有 uid」这条常见路径。
	first := &reverse.Account{ID: 500, Region: "global", Platform: "qoder_global",
		Credentials: map[string]any{"access_token": "dt-first-token-123456"}}
	second := &reverse.Account{ID: 501, Region: "global", Platform: "qoder_global",
		Credentials: map[string]any{"access_token": "dt-second-token-123456"}}
	body := `{"model":"dmodel","metadata":{"conversation_id":"conv-shared"},"messages":[{"role":"user","content":"turn"}]}`

	forwardOnce(t, provider, first, body)
	firstEnvelope := <-envelopes
	forwardOnce(t, provider, second, body)
	secondEnvelope := <-envelopes

	if firstEnvelope["session_id"] == secondEnvelope["session_id"] {
		t.Fatalf("one conversation must not share a session across accounts: %v", firstEnvelope["session_id"])
	}
	if !uuidShape.MatchString(stringValue(firstEnvelope["session_id"])) {
		t.Fatalf("session id is not a UUID: %v", firstEnvelope["session_id"])
	}
}
