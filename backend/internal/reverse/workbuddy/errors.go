package workbuddy

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// retryAfterCap keeps absurd upstream retry hints from parking an account.
const retryAfterCap = 2 * time.Hour

// upstreamError converts an upstream failure into *reverse.UpstreamError.
//
// The body is preserved as the message (with the business code and requestId
// surfaced explicitly) because the reference protocol encodes most failures as
// HTTP status + business code + natural language; rewriting them into a
// generic sentence destroys the only evidence users can forward upstream.
func upstreamError(status int, body string, header http.Header) *reverse.UpstreamError {
	out := reverse.NewUpstreamError(status, "", "")
	raw := strings.TrimSpace(body)
	defer func() { classifyResponse(out, status, raw) }()
	if raw == "" {
		out.Message = "empty upstream response"
		out.RetryAfter = parseRetryAfter(header, time.Now())
		return out
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal([]byte(raw), &payload); errUnmarshal != nil {
		out.Message = truncate(raw, 512)
		out.RetryAfter = parseRetryAfter(header, time.Now())
		return out
	}

	code := firstNonEmpty(
		stringField(payload, "code"),
		stringField(payload, "errCode"),
		stringField(payload, "error_code"),
	)
	message := firstNonEmpty(
		stringField(payload, "msg"),
		stringField(payload, "message"),
	)
	requestID := firstNonEmpty(stringField(payload, "requestId"), stringField(payload, "request_id"))

	if nested := nestedObject(payload, "error"); nested != nil {
		code = firstNonEmpty(code, stringField(nested, "code"), stringField(nested, "type"))
		message = firstNonEmpty(message, stringField(nested, "message"))
	}
	if message == "" {
		message = truncate(raw, 512)
	}
	// 11128 carries localized display text that explains what the user must do;
	// append it without replacing the upstream msg.
	if tips := localizedHint(payload); tips != "" && !strings.Contains(message, tips) {
		message += " — " + tips
	}
	if requestID != "" {
		message += " (requestId=" + requestID + ")"
	}
	out.Code = code
	out.Message = message
	out.RetryAfter = parseRetryAfter(header, time.Now())
	return out
}

func classifyResponse(out *reverse.UpstreamError, status int, raw string) {
	classified := Classify(status, raw)
	out.Kind = string(classified.Kind)
	switch classified.Kind {
	case reverse.FailureKindChannelRejected, reverse.FailureKindContentBlocked, reverse.FailureKindBadParams,
		reverse.FailureKindPromptTooLong, reverse.FailureKindImageInvalid:
		out.StatusCode = http.StatusBadRequest
	case reverse.FailureKindSessionDead:
		out.StatusCode = http.StatusUnauthorized
	case reverse.FailureKindHardCredit:
		out.StatusCode = http.StatusPaymentRequired
	case reverse.FailureKindSoftRate:
		out.StatusCode = http.StatusTooManyRequests
	}
	if reset := ParseRateReset(raw); reset.After(time.Now()) && out.RetryAfter == 0 {
		out.RetryAfter = clampRetryAfter(time.Until(reset))
	}
}

// localizedHint reads the CN/EN display text the 11128 channel-rejection body
// embeds under displayTips/displayMsg.
func localizedHint(payload map[string]any) string {
	for _, key := range []string{"displayTips", "displayMsg"} {
		nested := nestedObject(payload, key)
		if nested == nil {
			continue
		}
		for _, lang := range []string{"zh", "en", "zh-hant"} {
			if text := stringField(nested, lang); text != "" {
				return text
			}
		}
	}
	return ""
}

// parseRetryAfter reads the upstream's wait hints. HTTP-Date values and
// durations above the sanity cap are ignored.
func parseRetryAfter(header http.Header, now time.Time) time.Duration {
	if header == nil {
		return 0
	}
	if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
		if seconds, okParse := parseSeconds(raw); okParse {
			return clampRetryAfter(time.Duration(seconds) * time.Second)
		}
	}
	if raw := strings.TrimSpace(header.Get("Retry-After-Ms")); raw != "" {
		if millis, okParse := parseSeconds(raw); okParse {
			return clampRetryAfter(time.Duration(millis) * time.Millisecond)
		}
	}
	if raw := strings.TrimSpace(header.Get("X-Ratelimit-Reset")); raw != "" {
		if value, okParse := parseSeconds(raw); okParse {
			if len(raw) >= 12 {
				value /= 1000
			}
			if resetAt := time.Unix(value, 0); resetAt.After(now) {
				return clampRetryAfter(resetAt.Sub(now))
			}
		}
	}
	return 0
}

func parseSeconds(raw string) (int64, bool) {
	value, errParse := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value, errParse == nil && value > 0
}

func clampRetryAfter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > retryAfterCap {
		return retryAfterCap
	}
	return d
}

// isAlreadyCheckin reports the upstream's idempotent "checked in today" reply.
func isAlreadyCheckin(err error) bool {
	return IsAlreadyCheckin(err)
}
