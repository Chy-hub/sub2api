package workbuddy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// deriveSaltPrefix is the fixed salt prefix of device-ID derivation. Together
// with the account's machine_salt it keeps IDs stable across restarts while
// isolating independent deployments.
const deriveSaltPrefix = "wb2a:"

// credential is the normalized view of reverse.Account.Credentials.
//
// Both the project's flat shape (access_token / uid / enterprise_id) and the
// reference plugin's nested shape ({"auth":{"accessToken":...},"account":{...}})
// are accepted on read; flat keys win when both are present.
type credential struct {
	accessToken    string
	refreshToken   string
	uid            string
	nickname       string
	domain         string
	enterpriseID   string
	deviceToken    string
	machineIDv     string
	sessionIDv     string
	machineSalt    string
	region         region
	userAgent      string
	clientVersion  string
	cliVersion     string
	clientName     string
	desktopVersion string
}

// newCredential builds a credential snapshot. It never returns nil.
func newCredential(acc *reverse.Account) *credential {
	cred := &credential{region: normalizeRegion("")}
	if acc == nil {
		return cred
	}

	creds := acc.Credentials
	cred.accessToken = credentialValue(creds, "auth", "access_token", "accessToken")
	cred.refreshToken = credentialValue(creds, "auth", "refresh_token", "refreshToken")
	cred.uid = credentialValue(creds, "account", "uid", "user_id", "userId")
	cred.nickname = credentialValue(creds, "account", "nickname")
	cred.domain = credentialValue(creds, "auth", "domain")
	cred.enterpriseID = credentialValue(creds, "account", "enterprise_id", "enterpriseId")
	cred.deviceToken = credentialValue(creds, "auth", "device_token", "deviceToken", "workbuddy_device_token")
	cred.machineIDv = credentialValue(creds, "auth", "machine_id", "machineId")
	cred.sessionIDv = credentialValue(creds, "auth", "session_id", "sessionId")
	cred.machineSalt = credentialValue(creds, "auth", "machine_salt", "machineSalt")
	cred.userAgent = credentialValue(creds, "auth", "user_agent", "workbuddy_user_agent")
	cred.clientVersion = firstNonEmpty(credentialValue(creds, "auth", "client_version", "workbuddy_client_version"), clientVersion)
	cred.cliVersion = firstNonEmpty(credentialValue(creds, "auth", "cli_version", "workbuddy_cli_version"), cliVersion)
	cred.clientName = firstNonEmpty(credentialValue(creds, "auth", "client_name", "workbuddy_client_name"), clientName)
	cred.desktopVersion = firstNonEmpty(credentialValue(creds, "auth", "desktop_version"), DesktopClientVersion)

	regionRaw := strings.TrimSpace(acc.Region)
	if regionRaw == "" {
		regionRaw = credentialValue(creds, "auth", "realm")
	}
	if regionRaw != "" {
		cred.region = normalizeRegion(regionRaw)
	} else if inferred := regionFromDomain(cred.domain); inferred != "" {
		cred.region = inferred
	}
	return cred
}

func (c *credential) chatUserAgent() string {
	if c.userAgent != "" {
		return c.userAgent
	}
	return "WorkBuddy/" + firstNonEmpty(c.clientVersion, clientVersion) + " " + endpointsFor(c.region).platformLabel + "/" + firstNonEmpty(c.clientVersion, clientVersion) + " CLI/" + firstNonEmpty(c.cliVersion, cliVersion)
}

// credentialValue resolves a value from the flat credential map first, then
// from the nested section (if any).
func credentialValue(creds map[string]any, section string, keys ...string) string {
	if creds == nil {
		return ""
	}
	for _, key := range keys {
		if value := stringField(creds, key); value != "" {
			return value
		}
	}
	nested := nestedObject(creds, section)
	if nested == nil {
		return ""
	}
	for _, key := range keys {
		if value := stringField(nested, key); value != "" {
			return value
		}
	}
	return ""
}

// regionFromDomain infers the deployment from the account domain.
func regionFromDomain(domain string) region {
	trimmed := strings.ToLower(strings.TrimSpace(domain))
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" {
		return ""
	}
	if trimmed == "workbuddy.ai" || strings.HasSuffix(trimmed, ".workbuddy.ai") {
		return regionGlobal
	}
	return regionCN
}

// machineID returns the explicit machine id or the stable derived one.
func (c *credential) machineID() string {
	if c == nil {
		return ""
	}
	if c.machineIDv != "" {
		return c.machineIDv
	}
	return deriveID(c.uid, "machine", c.machineSalt)
}

// sessionID returns the explicit session id or the stable derived one.
func (c *credential) sessionID() string {
	if c == nil {
		return ""
	}
	if c.sessionIDv != "" {
		return c.sessionIDv
	}
	return deriveID(c.uid, "session", c.machineSalt)
}

// deriveID derives a stable per-account identifier:
// sha256("wb2a:" + purpose + ":" + uid + ":" + salt)[:18] hex.
func deriveID(uid, purpose, salt string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(deriveSaltPrefix + purpose + ":" + uid + ":" + salt))
	return hex.EncodeToString(sum[:18])
}

// buildCacheKey builds prompt_cache_key. The uid prefix hard-isolates accounts:
// sharing a cache key across accounts would let the upstream reuse another
// account's cached prefix.
func buildCacheKey(uid, conversation string) string {
	uidPrefix := uid
	if len(uidPrefix) > 8 {
		uidPrefix = uidPrefix[:8]
	}
	if uidPrefix == "" {
		uidPrefix = "-"
	}
	sum := sha256.Sum256([]byte(uid + "|" + conversation))
	return "wb2a-" + uidPrefix + "-" + hex.EncodeToString(sum[:16])
}

// conversationKey extracts the conversation identifier from a Chat Completions
// body: metadata.conversation_id, top-level conversation_id, prompt_cache_key,
// then a hash of the system + first user message.
func conversationKey(body []byte) string {
	payload := decodeBodyMap(body)
	if payload == nil {
		return ""
	}
	if metadata := nestedObject(payload, "metadata"); metadata != nil {
		for _, key := range []string{"conversation_id", "conversationId"} {
			if value := stringField(metadata, key); value != "" {
				return value
			}
		}
	}
	for _, key := range []string{"conversation_id", "conversationId"} {
		if value := stringField(payload, key); value != "" {
			return value
		}
	}
	if value := stringField(payload, "prompt_cache_key"); value != "" {
		return value
	}
	return deriveConversationKey(payload)
}

func deriveConversationKey(payload map[string]any) string {
	messages, okMessages := payload["messages"].([]any)
	if !okMessages || len(messages) == 0 {
		return ""
	}
	systemText := ""
	firstUser := ""
	for _, item := range messages {
		message, okMessage := item.(map[string]any)
		if !okMessage {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(stringField(message, "role"))) {
		case "system", "developer":
			if systemText == "" {
				systemText = contentSignature(message["content"])
			}
		case "user":
			if firstUser == "" {
				firstUser = contentSignature(message["content"])
			}
		}
	}
	if firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(systemText + "\x00" + firstUser))
	return "d-" + hex.EncodeToString(sum[:])[:16]
}

// contentSignature compresses message content into a stable short signature.
// Multimodal arrays keep the non-text part types so images never collide with
// plain text.
func contentSignature(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			part, okPart := item.(map[string]any)
			if !okPart {
				continue
			}
			if text, okText := part["text"].(string); okText {
				parts = append(parts, text)
				continue
			}
			partType := stringField(part, "type")
			sum := sha256.Sum256([]byte(partType))
			parts = append(parts, "["+partType+":"+hex.EncodeToString(sum[:4])+"]")
		}
		return strings.Join(parts, "\x1f")
	}
	return ""
}

// requestModel reads the model from a Chat Completions body.
func requestModel(body []byte) string {
	payload := decodeBodyMap(body)
	if payload == nil {
		return ""
	}
	return stringField(payload, "model")
}

// decodeBodyMap parses a JSON object; nil means "cannot rewrite".
func decodeBodyMap(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil
	}
	return payload
}

// randomHex returns n random bytes as hex. A failed CSPRNG falls back to a
// time-derived value so request IDs stay unique.
func randomHex(n int) string {
	buffer := make([]byte, n)
	if _, errRand := rand.Read(buffer); errRand == nil {
		return hex.EncodeToString(buffer)
	}
	sum := sha256.Sum256([]byte(time.Now().String()))
	return hex.EncodeToString(sum[:n])
}

// newUUIDv7Hex returns a hyphen-free UUIDv7 (32 hex chars). The genuine client
// validates conversation IDs against ^[0-9a-f]{12}7[0-9a-f]{3}[89ab][0-9a-f]{15}$.
func newUUIDv7Hex() string {
	var buf [16]byte
	if _, errRead := rand.Read(buf[:]); errRead != nil {
		return randomHex(16)
	}
	ms := time.Now().UnixMilli()
	buf[0] = byte(ms >> 40)
	buf[1] = byte(ms >> 32)
	buf[2] = byte(ms >> 24)
	buf[3] = byte(ms >> 16)
	buf[4] = byte(ms >> 8)
	buf[5] = byte(ms)
	buf[6] = (buf[6] & 0x0F) | 0x70
	buf[8] = (buf[8] & 0x3F) | 0x80
	return hex.EncodeToString(buf[:])
}

// todayCST returns the current date in the upstream's natural-day timezone.
func todayCST() string {
	return time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02")
}
