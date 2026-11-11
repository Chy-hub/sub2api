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
// The browser-context headers (Origin/Referer/Accept-Language) are kept here:
// the global client sends them on chat, and dropping them was one of the
// differences that made the upstream answer 11128. Only the device-derived
// identifiers are removed — those belong to the billing and feedback paths.
func applyChatHeaders(req *http.Request, cred *credential, meta chatMeta) {
	applyCommonHeaders(req, cred)
	applyAuthHeaders(req, cred)

	for _, key := range []string{
		"X-Device-Token", "X-Machine-ID", "X-Session-ID",
	} {
		req.Header.Del(key)
	}

	req.Header.Set("Accept", "application/json, text/event-stream")

	if cred.region.isGlobal() {
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

	applyAttributionHeaders(req, cred.region)
	req.Header.Set("X-IDE-Name", firstNonEmpty(cred.clientName, clientName))
	req.Header.Set("X-IDE-Type", firstNonEmpty(cred.clientName, clientName))
	req.Header.Set("X-IDE-Version", cred.clientVersion)
	applyConversationHeaders(req, meta)
}

// applyAttributionHeaders writes the client-attribution family the genuine
// client always sends.
//
// X-Private-Data differs by deployment: the global client reports true, the CN
// one false. X-IDE-Version follows the region's client version.
func applyAttributionHeaders(req *http.Request, reg region) {
	ep := endpointsFor(reg)
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-Agent-Type", "main")
	req.Header.Set("X-IDE-Name", clientName)
	req.Header.Set("X-IDE-Type", clientName)
	req.Header.Set("X-IDE-Version", ep.clientVersion)
	if reg.isGlobal() {
		req.Header.Set("X-Private-Data", "true")
	} else {
		req.Header.Set("X-Private-Data", "false")
	}
	req.Header.Set("X-Product", "SaaS")
}

// applyConversationHeaders writes the conversation and trace families.
//
// The upstream aggregates requests by these IDs and splits a conversation when
// any of them is missing or inconsistent; a missing conversation family is also
// one of the inputs to the 11128 channel check. The apparent redundancy
// (several headers share one value) is therefore intentional.
//
// X-Conversation-ID must be a canonical hyphenated UUID. An upstream-provided
// value that is not one is replaced rather than forwarded, because the check
// looks at shape, not at provenance.
//
// That replacement has a cost worth naming: conversationKey also yields
// prompt_cache_key and a derived hash, neither of which is a UUID, so those
// requests lose cross-request conversation grouping on the upstream side. The
// reference plugin makes the same trade — it validates the same way and
// regenerates on mismatch — and passing the shape through instead means the
// channel check rejects the request outright. A caller that needs stable
// grouping can send metadata.conversation_id as a UUID.
func applyConversationHeaders(req *http.Request, meta chatMeta) {
	messageID := strings.TrimSpace(meta.messageID)
	if messageID == "" {
		messageID = newUUIDv7Hex()
	}
	conversationRequestID := strings.TrimSpace(meta.conversationRequestID)
	if conversationRequestID == "" {
		conversationRequestID = messageID
	}
	conversationID := strings.TrimSpace(meta.conversationID)
	if !isCanonicalUUID(conversationID) {
		conversationID = newUUIDv7()
	}
	req.Header.Set("X-Conversation-ID", conversationID)
	req.Header.Set("X-Conversation-Request-ID", conversationRequestID)
	req.Header.Set("X-Root-Request-ID", conversationRequestID)
	req.Header.Set("X-Conversation-Message-ID", messageID)
	req.Header.Set("X-Request-ID", messageID)

	// The b3 single header is traceId-spanId-flags; the parent span only exists
	// on child spans, and inventing one is itself a fingerprint mismatch.
	traceID := randomHex(16)
	spanID := randomHex(8)
	req.Header.Set("traceparent", "00-"+traceID+"-"+spanID+"-01")
	req.Header.Set("b3", traceID+"-"+spanID+"-1")
	req.Header.Set("X-B3-TraceId", traceID)
	req.Header.Set("X-B3-SpanId", spanID)
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
	} else {
		// Personal accounts must declare "no enterprise" explicitly; some
		// billing endpoints treat a missing header as a malformed request.
		req.Header.Set("X-No-Enterprise-Id", "1")
	}
	// X-Domain prefers the account's recorded domain and falls back to the
	// request's target region rather than staying empty: the official client
	// always sends this header on billing calls.
	req.Header.Set("X-Domain", configDomain(cred))
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Product-Code", productCodeFor(cred.region))
	if cred.deviceToken != "" {
		req.Header.Set("X-Device-Token", cred.deviceToken)
	}
}

// productCodeFor returns the region's product code (the official client's
// X-Product-Code header).
func productCodeFor(reg region) string {
	if reg.isGlobal() {
		return "workbuddy"
	}
	return "codebuddy"
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

// userAgent builds the three-segment chat UA for a region.
//
// The version segments come from the region's endpoint table: the two
// deployments ship different genuine clients and the upstream fingerprints the
// UA against them.
func userAgent(reg region) string {
	ep := endpointsFor(reg)
	return "WorkBuddy/" + ep.clientVersion + " " + ep.platformLabel + "/" + ep.clientVersion + " CLI/" + ep.cliVersion
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
