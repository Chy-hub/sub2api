package workbuddy

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Channel rejection is a request error even when the upstream wraps it in a
// 500. Its original business code and actionable context must be preserved.
func TestUpstreamErrorPassesChannelRejection(t *testing.T) {
	body := `{"code":11128,"msg":"illegal api invocation","requestId":"req-1","displayTips":{"zh":"请使用官方客户端"},"displayMsg":{"en":"use the official client"}}`
	errUpstream := upstreamError(http.StatusInternalServerError, body, nil)
	if errUpstream.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", errUpstream.StatusCode)
	}
	if errUpstream.Code != "11128" {
		t.Fatalf("code = %q", errUpstream.Code)
	}
	for _, marker := range []string{"illegal api invocation", "请使用官方客户端", "requestId=req-1"} {
		if !strings.Contains(errUpstream.Message, marker) {
			t.Fatalf("message %q missing %q", errUpstream.Message, marker)
		}
	}

	illegal := upstreamError(http.StatusForbidden, `{"code":11140,"msg":"request illegal"}`, nil)
	if illegal.StatusCode != http.StatusForbidden || illegal.Code != "11140" {
		t.Fatalf("11140 must pass through: %+v", illegal)
	}
	if !strings.Contains(illegal.Message, "request illegal") {
		t.Fatalf("message = %q", illegal.Message)
	}
}

// TestUpstreamErrorRetryAfter verifies the upstream wait hints.
func TestUpstreamErrorRetryAfter(t *testing.T) {
	header := http.Header{}
	header.Set("Retry-After", "30")
	errUpstream := upstreamError(http.StatusTooManyRequests, `{"code":6004,"msg":"rate limited"}`, header)
	if errUpstream.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v", errUpstream.RetryAfter)
	}
}

// TestUpstreamErrorPlainText verifies non-JSON bodies are preserved.
func TestUpstreamErrorPlainText(t *testing.T) {
	errUpstream := upstreamError(http.StatusBadGateway, "  upstream exploded  ", nil)
	if errUpstream.Message != "upstream exploded" {
		t.Fatalf("message = %q", errUpstream.Message)
	}
}
