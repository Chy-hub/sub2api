package workbuddy

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// uuidV7HexRe is the genuine client's conversation-id assertion.
var uuidV7HexRe = regexp.MustCompile(`^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$`)

func testCredential(reg string) *credential {
	return newCredential(&reverse.Account{
		Region: reg,
		Credentials: map[string]any{
			"access_token": "token",
			"uid":          "a707ecea-6211-4e4e-a1b4-13335f8ed5bc",
			"device_token": "device-token",
			"machine_id":   "explicit-machine",
			"session_id":   "explicit-session",
			"machine_salt": "salt-1",
		},
	})
}

func newHeaderRequest(t *testing.T) *http.Request {
	t.Helper()
	req, errReq := http.NewRequest(http.MethodPost, "https://www.workbuddy.cn/v2/chat/completions", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	return req
}

// TestChatHeadersMatchGenuineClient asserts the chat matrix: three-segment UA,
// attribution family, UUIDv7 conversation family and W3C trace family, and no
// device fingerprint headers.
//
// X-Conversation-ID is asserted by shape, not by equality: the upstream
// validates it as a canonical UUID and rejects the request with 11128
// otherwise, so a caller-supplied non-UUID is replaced rather than forwarded.
func TestChatHeadersMatchGenuineClient(t *testing.T) {
	cred := testCredential("cn")
	req := newHeaderRequest(t)
	conversationID := newUUIDv7()
	applyChatHeaders(req, cred, chatMeta{conversationID: conversationID, conversationRequestID: newUUIDv7Hex()})

	if got := req.Header.Get("User-Agent"); got != "WorkBuddy/5.8.3 WorkBuddy/5.8.3 CLI/2.179.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	for key, want := range map[string]string{
		"X-Product":           "SaaS",
		"X-IDE-Type":          "WorkBuddy",
		"X-IDE-Name":          "WorkBuddy",
		"X-IDE-Version":       "5.8.3",
		"X-Agent-Intent":      "craft",
		"X-Agent-Purpose":     "conversation",
		"X-Agent-Type":        "main",
		"X-Private-Data":      "false",
		"X-User-Id":           cred.uid,
		"X-Domain":            "www.workbuddy.cn",
		"X-CodeBuddy-Request": "1",
		"Authorization":       "Bearer token",
		"X-Conversation-ID":   conversationID,
	} {
		if got := req.Header.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if !isCanonicalUUID(req.Header.Get("X-Conversation-ID")) {
		t.Fatalf("X-Conversation-ID = %q is not a canonical UUID", req.Header.Get("X-Conversation-ID"))
	}

	// Conversation family: UUIDv7 shapes, request/message ids share the value.
	for _, key := range []string{"X-Conversation-Request-ID", "X-Conversation-Message-ID", "X-Request-ID"} {
		if got := req.Header.Get(key); !uuidV7HexRe.MatchString(got) {
			t.Fatalf("%s = %q is not UUIDv7 hex", key, got)
		}
	}
	if req.Header.Get("X-Conversation-Message-ID") != req.Header.Get("X-Request-ID") {
		t.Fatal("message id and request id must match")
	}
	if req.Header.Get("X-Conversation-Request-ID") == req.Header.Get("X-Conversation-Message-ID") {
		t.Fatal("conversation request id must differ from the message id")
	}

	// Trace family: same-source random ids, not the conversation ids.
	traceID := req.Header.Get("X-Trace-ID")
	spanID := req.Header.Get("X-B3-SpanId")
	if len(traceID) != 32 || len(spanID) != 16 {
		t.Fatalf("trace id lengths = %d/%d", len(traceID), len(spanID))
	}
	if req.Header.Get("traceparent") != "00-"+traceID+"-"+spanID+"-01" {
		t.Fatalf("traceparent = %q", req.Header.Get("traceparent"))
	}
	// b3 single is traceId-spanId-flags. A parent span id only exists on child
	// spans, so inventing one is itself a fingerprint mismatch.
	if req.Header.Get("b3") != traceID+"-"+spanID+"-1" {
		t.Fatalf("b3 = %q", req.Header.Get("b3"))
	}
	if got := req.Header.Get("X-B3-ParentSpanId"); got != "" {
		t.Fatalf("X-B3-ParentSpanId must not be sent, got %q", got)
	}
	if req.Header.Get("X-B3-TraceId") != traceID || req.Header.Get("X-B3-Sampled") != "1" {
		t.Fatal("X-B3-TraceId/X-B3-Sampled mismatch")
	}
	if traceID == req.Header.Get("X-Conversation-Request-ID") {
		t.Fatal("trace id must not reuse the conversation request id")
	}
	// X-Root-Request-ID is part of the conversation family the channel check
	// expects, and shares the round's request id.
	if got := req.Header.Get("X-Root-Request-ID"); got != req.Header.Get("X-Conversation-Request-ID") {
		t.Fatalf("X-Root-Request-ID = %q, want the conversation request id", got)
	}

	// Browser context is kept; only the device-derived identifiers are dropped.
	for key, want := range map[string]string{
		"Origin":          "https://www.workbuddy.cn",
		"Referer":         "https://www.workbuddy.cn/",
		"Accept-Language": "zh-CN",
	} {
		if got := req.Header.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{
		"X-Device-Token", "X-Machine-ID", "X-Session-ID",
	} {
		if got := req.Header.Get(key); got != "" {
			t.Fatalf("%s must not be sent on chat, got %q", key, got)
		}
	}
	if !strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		t.Fatalf("Accept = %q", req.Header.Get("Accept"))
	}
}

// TestChatReplacesNonUUIDConversationIDs locks the trade the channel check
// forces. conversationKey yields prompt_cache_key and derived hashes as well as
// real UUIDs, and a non-UUID X-Conversation-ID is rejected outright, so the
// header is regenerated instead of forwarded. The consequence is that such
// requests lose upstream conversation grouping — accepted deliberately, and
// worth a failing test if someone later tries to pass the raw value through.
func TestChatReplacesNonUUIDConversationIDs(t *testing.T) {
	cred := testCredential("cn")
	for _, supplied := range []string{"cache-key-abc", "d-4f2a9c", ""} {
		req := newHeaderRequest(t)
		applyChatHeaders(req, cred, chatMeta{conversationID: supplied})
		got := req.Header.Get("X-Conversation-ID")
		if !isCanonicalUUID(got) {
			t.Fatalf("X-Conversation-ID from %q = %q, want a canonical UUID", supplied, got)
		}
		if supplied != "" && got == supplied {
			t.Fatalf("non-UUID %q was forwarded unchanged", supplied)
		}
	}

	// A caller that supplies a real UUID keeps it, which is how stable grouping
	// stays available.
	chosen := newUUIDv7()
	req := newHeaderRequest(t)
	applyChatHeaders(req, cred, chatMeta{conversationID: chosen})
	if got := req.Header.Get("X-Conversation-ID"); got != chosen {
		t.Fatalf("valid conversation id was replaced: %q != %q", got, chosen)
	}
}

// TestBillingHeadersCarryDeviceHeaders asserts billing/task calls keep the
// device fingerprint set and a single-segment UA.
func TestBillingHeadersCarryDeviceHeaders(t *testing.T) {
	cred := testCredential("cn")
	cred.domain = "www.codebuddy.cn"
	req := newHeaderRequest(t)
	applyBillingHeaders(req, cred)

	for key, want := range map[string]string{
		"User-Agent":      "WorkBuddy/5.8.3",
		"X-Device-Token":  "device-token",
		"X-Machine-ID":    "explicit-machine",
		"X-Session-ID":    "explicit-session",
		"X-Domain":        "www.codebuddy.cn",
		// The CN client's chat/device origin is workbuddy.cn even though the
		// billing domain stays codebuddy.cn — these are two deployments.
		"Origin":          "https://www.workbuddy.cn",
		"Referer":         "https://www.workbuddy.cn/",
		"Accept-Language": "zh-CN",
	} {
		if got := req.Header.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if req.Header.Get("traceparent") != "" {
		t.Fatal("billing calls must not carry the trace family")
	}
}

// TestDerivedMachineHeadersWhenUnset verifies the derived fallback IDs.
func TestDerivedMachineHeadersWhenUnset(t *testing.T) {
	cred := newCredential(&reverse.Account{
		Region:      "cn",
		Credentials: map[string]any{"uid": "u1", "machine_salt": "salt"},
	})
	req := newHeaderRequest(t)
	applyBillingHeaders(req, cred)
	if got := req.Header.Get("X-Machine-ID"); got != deriveID("u1", "machine", "salt") || len(got) != 36 {
		t.Fatalf("X-Machine-ID = %q", got)
	}
	if got := req.Header.Get("X-Session-ID"); got != deriveID("u1", "session", "salt") {
		t.Fatalf("X-Session-ID = %q", got)
	}
	if req.Header.Get("Authorization") != "" || req.Header.Get("X-No-Authorization") != "1" {
		t.Fatal("missing token must use X-No-Authorization")
	}
}

// TestGlobalChatHeaders asserts global-realm tenant and UA differences.
//
// The global client reports its own 5.8.4 / CLI 2.161.0 versions and the lower
// case platform slug. It does not send X-No-Enterprise-Id on chat: the global
// chatter path omits the enterprise family entirely rather than declaring
// "no enterprise".
func TestGlobalChatHeaders(t *testing.T) {
	cred := testCredential("global")
	req := newHeaderRequest(t)
	applyChatHeaders(req, cred, chatMeta{})

	if got := req.Header.Get("User-Agent"); got != "WorkBuddy/5.8.4 workbuddy-ai/5.8.4 CLI/2.161.0" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := req.Header.Get("X-Domain"); got != "www.workbuddy.ai" {
		t.Fatalf("X-Domain = %q", got)
	}
	if got := req.Header.Get("X-IDE-Version"); got != "5.8.4" {
		t.Fatalf("X-IDE-Version = %q, want the global client version", got)
	}
	// The global client reports private data as true; CN reports false.
	if got := req.Header.Get("X-Private-Data"); got != "true" {
		t.Fatalf("X-Private-Data = %q, want true on global", got)
	}
	if req.Header.Get("X-Enterprise-Id") != "" {
		t.Fatal("global chat must not send X-Enterprise-Id")
	}
}

// TestCNChatTenantHeaders verifies enterprise propagation.
func TestCNChatTenantHeaders(t *testing.T) {
	cred := testCredential("cn")
	cred.enterpriseID = "ent-1"
	req := newHeaderRequest(t)
	applyChatHeaders(req, cred, chatMeta{})
	if req.Header.Get("X-Enterprise-Id") != "ent-1" || req.Header.Get("X-Tenant-Id") != "ent-1" {
		t.Fatal("CN enterprise headers missing")
	}
}

// TestConfigHeaders asserts the model-probe matrix.
func TestConfigHeaders(t *testing.T) {
	cred := testCredential("cn")
	req := newHeaderRequest(t)
	applyConfigHeaders(req, cred, codeBuddyIDEUA)

	for key, want := range map[string]string{
		"User-Agent":     codeBuddyIDEUA,
		"X-Product":      "SaaS",
		"X-Domain":       "www.workbuddy.cn",
		"X-Device-Token": "device-token",
		"Accept":         "application/json, text/plain, */*",
	} {
		if got := req.Header.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
