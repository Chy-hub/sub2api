package reverse

import "net/http"

// Preserve request correlation across synthetic OpenAI responses without
// forwarding upstream cookies or provider-specific authentication headers.
func CopyCorrelationHeaders(dst, src http.Header) {
	for _, key := range []string{"X-Request-ID", "Request-ID", "X-Upstream-Request-ID", "X-Trace-ID"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}
