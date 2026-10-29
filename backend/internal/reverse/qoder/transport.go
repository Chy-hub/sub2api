package qoder

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// maxShortResponseBytes bounds non-streaming responses (model list, userinfo,
// check-in, token exchange, login poll). A larger body means the upstream
// returned an HTML error page or a proxy injected content.
const maxShortResponseBytes int64 = 1 << 20

func readBodyLimited(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxShortResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxShortResponseBytes {
		return nil, fmt.Errorf("qoder response body exceeds %d bytes", maxShortResponseBytes)
	}
	return data, nil
}

// redactSecrets removes credential material from anything that may be surfaced
// to a client or persisted in a log.
func redactSecrets(message string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if len(secret) < 8 {
			continue
		}
		message = strings.ReplaceAll(message, secret, "[redacted]")
	}
	return message
}

// upstreamError converts a non-2xx upstream response into the project's
// structured error. Token material is always redacted.
func upstreamError(status int, header http.Header, body []byte, secrets ...string) *reverse.UpstreamError {
	if signal, queued := ParseQueueSignal(string(body)); queued {
		return queueSignalError(signal)
	}
	code, message := parseUpstreamErrorMessage(body)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if code == "" {
		switch status {
		case http.StatusUnauthorized:
			code = "unauthorized"
		case http.StatusTooManyRequests:
			code = "rate_limited"
		default:
			code = "upstream_error"
		}
	}
	message = redactSecrets(truncateRunes(message, 500), secrets...)
	err := reverse.NewUpstreamError(status, code, message)
	classifyQoderRequestError(err)
	if err.RetryAfter == 0 && header != nil {
		if seconds := intValue(header.Get("Retry-After")); seconds > 0 {
			err.RetryAfter = time.Duration(seconds) * time.Second
		}
	}
	return err
}

func classifyQoderRequestError(err *reverse.UpstreamError) {
	detail := err.Code + " " + err.Message
	for _, marker := range []string{"DataInspectionFailed", "inappropriate content", "input text data may contain", "ContentFilter", "SensitiveContent"} {
		if strings.Contains(detail, marker) {
			err.StatusCode = http.StatusBadRequest
			err.Code = "content_policy_rejected"
			err.Kind = string(reverse.FailureKindContentBlocked)
			return
		}
	}
	for _, marker := range []string{"invalid_parameter_error", "invalid_request_error", `"Range of `} {
		if strings.Contains(detail, marker) {
			err.StatusCode = http.StatusBadRequest
			err.Kind = string(reverse.FailureKindBadParams)
			return
		}
	}
}

func streamBusinessError(code, message, payload string) *reverse.UpstreamError {
	if signal, queued := ParseQueueSignal(payload); queued {
		return queueSignalError(signal)
	}
	err := reverse.NewUpstreamError(http.StatusBadGateway, code, message)
	err.Kind = string(reverse.FailureKindClient)
	classifyQoderRequestError(err)
	return err
}

func parseUpstreamErrorMessage(body []byte) (code, message string) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return "", ""
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(trimmed), &parsed) != nil {
		return "", trimmed
	}
	code = stringValue(parsed["code"])
	message = firstNonEmpty(
		stringValue(parsed["message"]),
		stringValue(parsed["msg"]),
		stringValue(parsed["error_description"]),
		stringValue(parsed["detail"]),
	)
	switch value := parsed["error"].(type) {
	case string:
		if message == "" {
			message = value
		}
	case map[string]any:
		if message == "" {
			message = firstNonEmpty(stringValue(value["message"]), stringValue(value["detail"]))
		}
		if code == "" {
			code = firstNonEmpty(stringValue(value["code"]), stringValue(value["type"]))
		}
	}
	// Upstream often nests another JSON document in message.
	if strings.HasPrefix(message, "{") {
		var nested map[string]any
		if json.Unmarshal([]byte(message), &nested) == nil {
			if inner := firstNonEmpty(stringValue(nested["message"]), stringValue(nested["msg"])); inner != "" {
				message = inner
			}
			if code == "" {
				code = stringValue(nested["code"])
			}
		}
	}
	return code, message
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// decodeJSONResponse accepts both plain JSON and the custom-base64 encoding
// used by `?Encode=1` endpoints.
func decodeJSONResponse(data []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, fmt.Errorf("empty qoder response")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
		return parsed, nil
	}
	encoded := trimmed
	if strings.HasPrefix(encoded, `"`) {
		var unquoted string
		if err := json.Unmarshal([]byte(encoded), &unquoted); err == nil {
			encoded = strings.TrimSpace(unquoted)
		}
	}
	decoded, err := decodeCustomBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode qoder response: %w", err)
	}
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		return nil, fmt.Errorf("decode qoder response json: %w", err)
	}
	return parsed, nil
}
