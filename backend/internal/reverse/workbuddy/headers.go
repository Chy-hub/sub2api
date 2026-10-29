package workbuddy

import (
	"net/http"
	"net/url"
	"strings"
)

// chatMeta is the per-request conversation header family. Callers build it
// once per forward attempt; retries outside the provider reuse it.
type chatMeta struct {
	conversationID        string
	conversationRequestID string
	messageID             string
}

// applyCommonHeaders writes the headers shared by every API call. X-CodeBuddy-Request
// is one of the upstream's base judgments that a request comes from an official
// client; missing it triggers the generic 403 code=11140.
func applyCommonHeaders(req *http.Request, cred *credential) {
	ep := endpointsFor(cred.region)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", ep.origin)
	req.Header.Set("Referer", ep.origin+"/")
	req.Header.Set("User-Agent", cred.chatUserAgent())
	req.Header.Set("Accept-Language", ep.acceptLanguage)
	req.Header.Set("X-CodeBuddy-Request", "1")

	if cred.uid != "" {
		req.Header.Set("X-Machine-ID", cred.machineID())
		req.Header.Set("X-Session-ID", cred.sessionID())
	}
}

// applyAuthHeaders writes credential-derived headers. Missing credentials use
// the upstream's X-No-* markers instead of an empty Bearer so the upstream can
// answer with an accurate error.
func applyAuthHeaders(req *http.Request, cred *credential) {
	if cred.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cred.accessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if cred.uid != "" {
		req.Header.Set("X-User-Id", cred.uid)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
}

// applyChatHeaders writes the chat request matrix.
//
// The genuine CLI path deliberately omits Origin/Referer/Accept-Language and
// the device fingerprint headers (X-Device-Token/X-Machine-ID/X-Session-ID);
// the common headers add them, so they are removed here explicitly.
func applyChatHeaders(req *http.Request, cred *credential, meta chatMeta) {
	applyCommonHeaders(req, cred)
	applyAuthHeaders(req, cred)

	for _, key := range []string{
		"Origin", "Referer", "Accept-Language",
		"X-Device-Token", "X-Machine-ID", "X-Session-ID",
	} {
		req.Header.Del(key)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")

	if cred.region.isGlobal() {
		req.Header.Set("X-No-Enterprise-Id", "1")
		req.Header.Set("X-Domain", endpointHost(endpointsFor(regionGlobal).chatBase))
	} else {
		if cred.enterpriseID != "" {
			req.Header.Set("X-Enterprise-Id", cred.enterpriseID)
			req.Header.Set("X-Tenant-Id", cred.enterpriseID)
		} else {
			req.Header.Set("X-No-Enterprise-Id", "1")
		}
		req.Header.Set("X-Domain", endpointHost(endpointsFor(cred.region).chatBase))
	}

	applyAttributionHeaders(req)
	req.Header.Set("X-IDE-Name", firstNonEmpty(cred.clientName, clientName))
	req.Header.Set("X-IDE-Type", firstNonEmpty(cred.clientName, clientName))
	req.Header.Set("X-IDE-Version", firstNonEmpty(cred.clientVersion, clientVersion))
	applyConversationHeaders(req, meta)
}

// applyAttributionHeaders writes the client-attribution family the genuine
// client always sends.
func applyAttributionHeaders(req *http.Request) {
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-IDE-Name", clientName)
	req.Header.Set("X-IDE-Type", clientName)
	req.Header.Set("X-IDE-Version", clientVersion)
	req.Header.Set("X-Private-Data", "false")
	req.Header.Set("X-Product", "SaaS")
}

// applyConversationHeaders writes the conversation and trace families.
//
// The apparent redundancy (several headers share one value) is intentional:
// the upstream aggregates requests by these IDs and splits a conversation when
// any of them is missing or inconsistent.
func applyConversationHeaders(req *http.Request, meta chatMeta) {
	messageID := strings.TrimSpace(meta.messageID)
	if messageID == "" {
		messageID = newUUIDv7Hex()
	}
	conversationRequestID := strings.TrimSpace(meta.conversationRequestID)
	if conversationRequestID == "" {
		conversationRequestID = messageID
	}
	if conversationID := strings.TrimSpace(meta.conversationID); conversationID != "" {
		req.Header.Set("X-Conversation-ID", conversationID)
	}
	req.Header.Set("X-Conversation-Request-ID", conversationRequestID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)

	traceID := randomHex(16)
	spanID := randomHex(8)
	parentSpanID := randomHex(8)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	req.Header.Set("b3", traceID+"-"+spanID+"-1-"+parentSpanID)
	req.Header.Set("X-B3-TraceId", traceID)
	req.Header.Set("X-B3-SpanId", spanID)
	req.Header.Set("X-B3-ParentSpanId", parentSpanID)
	req.Header.Set("X-B3-Sampled", "1")
	req.Header.Set("X-Trace-ID", traceID)
}

// applyBillingHeaders writes the billing/task domain matrix: no conversation
// or attribution family, a single-segment UA and the device fingerprint set.
func applyBillingHeaders(req *http.Request, cred *credential) {
	applyCommonHeaders(req, cred)
	applyAuthHeaders(req, cred)

	req.Header.Set("User-Agent", firstNonEmpty(cred.userAgent, "WorkBuddy/"+firstNonEmpty(cred.clientVersion, clientVersion)))

	if cred.enterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", cred.enterpriseID)
		req.Header.Set("X-Tenant-Id", cred.enterpriseID)
	}
	if cred.domain != "" {
		req.Header.Set("X-Domain", cred.domain)
	}
	if cred.deviceToken != "" {
		req.Header.Set("X-Device-Token", cred.deviceToken)
	}
}

// applyConfigHeaders writes the model-probe matrix. The IDE UA is the risk gate
// on /v3/config: an outdated UA yields a reduced catalog or 400 code=12403.
func applyConfigHeaders(req *http.Request, cred *credential, userAgent string) {
	applyCommonHeaders(req, cred)
	applyAuthHeaders(req, cred)

	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Domain", configDomain(cred))
	if cred.deviceToken != "" {
		req.Header.Set("X-Device-Token", cred.deviceToken)
	}
}

// applyLoginHeaders writes the device-authorization matrix.
func applyLoginHeaders(req *http.Request, reg region) {
	ep := endpointsFor(reg)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", ep.origin)
	req.Header.Set("Referer", ep.origin+"/")
	req.Header.Set("User-Agent", loginClientUA)
	req.Header.Set("X-CodeBuddy-Request", "1")
}

// userAgent builds the three-segment chat UA.
func userAgent(reg region) string {
	platform := endpointsFor(reg).platformLabel
	return "WorkBuddy/" + clientVersion + " " + platform + "/" + clientVersion + " CLI/" + cliVersion
}

// configDomain resolves X-Domain for model probes: the account domain when
// present, otherwise the region's chat host.
func configDomain(cred *credential) string {
	domain := strings.TrimSpace(cred.domain)
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimSuffix(domain, "/")
	if domain != "" {
		return domain
	}
	base := endpointsFor(cred.region).chatBase
	base = strings.TrimPrefix(base, "https://")
	base = strings.TrimPrefix(base, "http://")
	return strings.TrimSuffix(base, "/")
}

// endpointHost extracts the host (with port) of a base URL.
func endpointHost(base string) string {
	parsed, errParse := url.Parse(strings.TrimSpace(base))
	if errParse != nil {
		return ""
	}
	return parsed.Host
}
