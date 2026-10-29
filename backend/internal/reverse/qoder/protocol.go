// Package qoder adapts the Qoder (qoder.com / qoder.com.cn) reverse-proxy
// protocol to the project's reverse provider boundary.
//
// The protocol layer (cosy session crypto, request signing, custom base64,
// agent chat envelope, SSE deltas, machine identity, device login, campaigns)
// is ported from the MIT-licensed CLIProxyAPI-freetier2api-plugin
// (internal/vendors/qoder). The adapter/config/host plumbing of the plugin is
// intentionally not copied.
package qoder

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "embed"
)

// basePromptRaw is the captured desktop request template. It is ported
// verbatim from the MIT-licensed CLIProxyAPI-freetier2api-plugin
// (internal/vendors/qoder/assets/baseprompt.json); see assets/NOTICE.
//
//go:embed assets/baseprompt.json
var basePromptRaw []byte

const (
	qoderVersion   = "1.0.10"
	desktopVersion = "0.4.8"

	appCode       = "cosy"
	legacySecret  = "d2FyLCB3YXIgbmV2ZXIgY2hhbmdlcw==" // base64("war, war never changes")
	oauthClientID = "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"
)

var serverPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// region selects the deployment (and therefore the endpoint table) of an
// account.
type region string

const (
	regionGlobal region = "global"
	regionCN     region = "cn"
)

func normalizeRegion(value string) region {
	if strings.EqualFold(strings.TrimSpace(value), string(regionCN)) {
		return regionCN
	}
	return regionGlobal
}

// endpoints is the per-region Qoder endpoint table.
type endpoints struct {
	deviceLogin string
	poll        string
	userinfo    string
	plan        string
	quota       string
	chat        string
	modelList   string
	jobToken    string
	sashBase    string
}

var endpointsGlobal = endpoints{
	deviceLogin: "https://qoder.com/device/selectAccounts",
	poll:        "https://openapi.qoder.sh/api/v1/deviceToken/poll",
	userinfo:    "https://openapi.qoder.sh/api/v1/userinfo",
	plan:        "https://openapi.qoder.sh/api/v2/user/plan",
	quota:       "https://openapi.qoder.sh/api/v2/quota/usage",
	chat:        "https://api1.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
	modelList:   "https://api2.qoder.sh/algo/api/v2/model/list?Encode=1",
	jobToken:    "https://center.qoder.sh/algo/api/v3/user/jobToken?Encode=1",
	sashBase:    "https://openapi.qoder.sh",
}

var endpointsCN = endpoints{
	deviceLogin: "https://qoder.com.cn/device/selectAccounts",
	poll:        "https://openapi.qoder.com.cn/api/v1/deviceToken/poll",
	userinfo:    "https://openapi.qoder.com.cn/api/v1/userinfo",
	plan:        "https://openapi.qoder.com.cn/api/v2/user/plan",
	quota:       "https://openapi.qoder.com.cn/api/v2/quota/usage",
	chat:        "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
	modelList:   "https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1",
	jobToken:    "https://gateway.qoder.com.cn/algo/api/v3/user/jobToken?Encode=1",
	sashBase:    "https://openapi.qoder.com.cn",
}

func endpointsFor(r region) endpoints {
	if r == regionCN {
		return endpointsCN
	}
	return endpointsGlobal
}

// ---------------------------------------------------------------------------
// Custom base64
// ---------------------------------------------------------------------------

const (
	customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	stdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customPadding  = '$'
)

var (
	customToStd [128]int
	stdToCustom [128]int
)

func init() {
	for i := range customToStd {
		customToStd[i] = -1
		stdToCustom[i] = -1
	}
	for i := 0; i < 64; i++ {
		customToStd[customAlphabet[i]] = int(stdAlphabet[i])
		stdToCustom[stdAlphabet[i]] = int(customAlphabet[i])
	}
	customToStd[customPadding] = '='
	stdToCustom['='] = customPadding
}

// encodeCustomBase64 is Qoder's shuffled, re-blocked base64 used for request
// bodies and some responses.
func encodeCustomBase64(plaintext []byte) (string, error) {
	std := base64.StdEncoding.EncodeToString(plaintext)
	n := len(std)
	a := n / 3
	rearranged := std[n-a:] + std[a:n-a] + std[:a]
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		c := int(rearranged[i])
		if c >= 128 || stdToCustom[c] < 0 {
			return "", fmt.Errorf("char out of alphabet: %d", c)
		}
		out[i] = byte(stdToCustom[c])
	}
	return string(out), nil
}

// decodeCustomBase64 reverses encodeCustomBase64.
func decodeCustomBase64(encoded string) ([]byte, error) {
	n := len(encoded)
	mapped := make([]byte, n)
	for i := 0; i < n; i++ {
		c := int(encoded[i])
		if c >= 128 || customToStd[c] < 0 {
			return nil, fmt.Errorf("char out of custom alphabet: %d", c)
		}
		mapped[i] = byte(customToStd[c])
	}
	a := n / 3
	std := string(mapped[n-a:]) + string(mapped[a:n-a]) + string(mapped[:a])
	return base64.StdEncoding.DecodeString(std)
}

// ---------------------------------------------------------------------------
// Signing and identifiers
// ---------------------------------------------------------------------------

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return fmt.Sprintf("%x", sum)
}

// signRequest signs one cosy request: md5(payload + key + date + body + path).
func signRequest(payloadB64, cosyKey, cosyDate, body, pathWithoutAlgo string) string {
	return md5Hex(payloadB64 + "\n" + cosyKey + "\n" + cosyDate + "\n" + body + "\n" + pathWithoutAlgo)
}

// signLegacy signs the jobToken exchange headers.
func signLegacy(date string) string {
	return md5Hex(appCode + "&" + legacySecret + "&" + date)
}

func pathSignature(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	path := parsed.Path
	if strings.HasPrefix(path, "/algo") {
		path = strings.TrimPrefix(path, "/algo")
	}
	return path, nil
}

func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

func newRequestID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", buf)[:24]
}

func randomHex(n int) string {
	if n <= 0 {
		n = 16
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func unixSec() int64 { return time.Now().Unix() }

func unixMs() int64 { return time.Now().UnixMilli() }

func currentDate() string { return time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT") }

// ---------------------------------------------------------------------------
// Machine identity
//
// Chat requests need a stable derived device identity. Check-in never derives
// one: the upstream validates the exact pair issued by the official desktop
// client, so only user-supplied values are sent (see checkin.go).
// ---------------------------------------------------------------------------

func deriveID(seed, salt string) string {
	sum := md5.Sum([]byte(salt + ":" + seed))
	return fmt.Sprintf("%x", sum)
}

func deriveMachineID(seed string) string { return deriveID(seed, "machine") }

func deriveMachineType(seed string) string { return deriveID(seed, "machinetype")[:18] }

func deriveMachineToken(seed string) string {
	primary := sha512.Sum512([]byte("machinetoken:" + seed))
	secondary := sha512.Sum512([]byte("machinetoken2:" + seed))
	raw := make([]byte, 66)
	copy(raw, primary[:])
	raw[64] = secondary[0]
	raw[65] = secondary[1]
	return base64.RawURLEncoding.EncodeToString(raw)[:88]
}

// ---------------------------------------------------------------------------
// Cosy session
// ---------------------------------------------------------------------------

type authIdentity struct {
	Name               string
	Aid                string
	Uid                string
	YxUid              string
	OrganizationID     string
	OrganizationName   string
	UserType           string
	SecurityOauthToken string
	RefreshToken       string
}

type sessionContext struct {
	CosyKey      string
	Info         string
	Identity     authIdentity
	MachineID    string
	MachineToken string
	MachineType  string
}

func newSession(identity authIdentity, machineID, machineToken, machineType string) (*sessionContext, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	tempKey := []byte(fmt.Sprintf("%x", raw)[:16])

	cosyKeyBytes, err := rsaEncrypt(tempKey)
	if err != nil {
		return nil, err
	}
	payloadBytes, err := json.Marshal(map[string]string{
		"name":                 identity.Name,
		"aid":                  identity.Aid,
		"uid":                  identity.Uid,
		"yx_uid":               identity.YxUid,
		"organization_id":      identity.OrganizationID,
		"organization_name":    identity.OrganizationName,
		"user_type":            identity.UserType,
		"security_oauth_token": identity.SecurityOauthToken,
		"refresh_token":        identity.RefreshToken,
	})
	if err != nil {
		return nil, err
	}
	encrypted, err := aesEncrypt(payloadBytes, tempKey)
	if err != nil {
		return nil, err
	}
	return &sessionContext{
		CosyKey:      base64.StdEncoding.EncodeToString(cosyKeyBytes),
		Info:         base64.StdEncoding.EncodeToString(encrypted),
		Identity:     identity,
		MachineID:    machineID,
		MachineToken: machineToken,
		MachineType:  machineType,
	}, nil
}

func rsaEncrypt(plaintext []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(serverPubKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("decode server public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("server public key is not RSA")
	}
	return rsa.EncryptPKCS1v15(rand.Reader, rsaPub, plaintext)
}

func aesEncrypt(plain, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	size := block.BlockSize()
	padding := size - len(plain)%size
	padded := make([]byte, len(plain)+padding)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padding)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:size]).CryptBlocks(out, padded)
	return out, nil
}

// buildPayloadB64 builds the authorization payload embedded in every cosy
// bearer token.
func buildPayloadB64(info string) (string, error) {
	data, err := json.Marshal(map[string]string{
		"cosyVersion": qoderVersion,
		"ideVersion":  "",
		"info":        info,
		"requestId":   newUUID(),
		"version":     "v1",
	})
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func composeBearer(payloadB64, signature string) string {
	return "Bearer COSY." + payloadB64 + "." + signature
}

// buildHeaders assembles the desktop-like cosy headers for one request.
func buildHeaders(sess *sessionContext, pathSig, body, accept string, extra map[string]string) (map[string]string, error) {
	payloadB64, err := buildPayloadB64(sess.Info)
	if err != nil {
		return nil, err
	}
	date := fmt.Sprintf("%d", unixSec())
	headers := map[string]string{
		"cosy-data-policy":      "agree",
		"content-type":          "application/json",
		"cosy-machinetype":      sess.MachineType,
		"cosy-clienttype":       "5",
		"cosy-date":             date,
		"cosy-user":             sess.Identity.Uid,
		"cosy-key":              sess.CosyKey,
		"cache-control":         "no-cache",
		"accept":                accept,
		"authorization":         composeBearer(payloadB64, signRequest(payloadB64, sess.CosyKey, date, body, pathSig)),
		"cosy-version":          qoderVersion,
		"cosy-machineid":        sess.MachineID,
		"cosy-machinetoken":     sess.MachineToken,
		"login-version":         "v2",
		"user-agent":            "Go-http-client/2.0",
		"cosy-scene":            "assistant",
		"cosy-business-product": "ide",
		"cosy-business-type":    "agent",
	}
	for key, value := range extra {
		headers[key] = value
	}
	return headers, nil
}

// ---------------------------------------------------------------------------
// Request template and Chat Completions envelope
// ---------------------------------------------------------------------------

// templateBase parses the embedded desktop template, replacing the captured
// dynamic placeholders.
func templateBase() (map[string]any, error) {
	template := string(basePromptRaw)
	for _, placeholder := range []string{"{UUID1}", "{UUID2}", "{UUID3}", "{UUID4}", "{UUID5}"} {
		template = strings.ReplaceAll(template, placeholder, newUUID())
	}
	template = strings.ReplaceAll(template, "{TIME1}", fmt.Sprintf("%d", unixMs()))

	var parsed map[string]any
	if err := json.Unmarshal([]byte(template), &parsed); err != nil {
		return nil, fmt.Errorf("parse embedded baseprompt template: %w", err)
	}
	if parsed == nil {
		return nil, fmt.Errorf("embedded baseprompt template is empty")
	}
	return parsed, nil
}

type chatRequest struct {
	model        string
	messages     []any
	tools        []any
	toolsEnabled bool
	maxTokens    int
	includeUsage bool
}

func decodeChatRequest(payload []byte) (*chatRequest, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode chat completions request: %w", err)
	}
	req := &chatRequest{model: strings.TrimSpace(stringValue(raw["model"]))}
	if req.model == "" {
		req.model = "auto"
	}
	req.messages, _ = raw["messages"].([]any)
	if tools, ok := raw["tools"].([]any); ok {
		req.tools = tools
		req.toolsEnabled = len(tools) > 0
	}
	req.maxTokens = intValue(raw["max_completion_tokens"])
	if req.maxTokens <= 0 {
		req.maxTokens = intValue(raw["max_tokens"])
	}
	if options, ok := raw["stream_options"].(map[string]any); ok {
		req.includeUsage, _ = options["include_usage"].(bool)
	}
	return req, nil
}

// buildEnvelope turns a standard Chat Completions request into the Qoder
// agent_chat_generation envelope, mirroring the desktop template.
func buildEnvelope(template map[string]any, req *chatRequest, model, userType string) map[string]any {
	body := deepCopyMap(template)

	id := newUUID()
	body["request_id"] = id
	body["chat_record_id"] = id
	body["request_set_id"] = newUUID()
	body["session_id"] = newUUID()
	body["stream"] = true
	body["aliyun_user_type"] = userType

	if modelConfig, ok := body["model_config"].(map[string]any); ok {
		modelConfig["key"] = model
	}

	if req.maxTokens > 0 {
		if params, ok := body["parameters"].(map[string]any); ok {
			params["max_tokens"] = req.maxTokens
		}
	}

	messages := buildQoderMessages(templateMessages(template), req.messages, extractLatestUserPrompt(req.messages), req.toolsEnabled)
	prompt := lastUserText(messages)
	if business, ok := body["business"].(map[string]any); ok {
		business["id"] = newUUID()
		business["begin_at"] = unixMs()
		business["name"] = truncateRunes(prompt, 30)
	}
	if context, ok := body["chat_context"].(map[string]any); ok {
		if text, ok := context["text"].(map[string]any); ok {
			text["text"] = prompt
		}
		if extra, ok := context["extra"].(map[string]any); ok {
			if original, ok := extra["originalContent"].(map[string]any); ok {
				original["text"] = prompt
			}
		}
	}

	body["messages"] = messages

	// The captured template ships 14 desktop tools. They must never leak into
	// requests whose client did not declare tools: the upstream would then be
	// free to answer with tool calls the caller cannot handle.
	if req.toolsEnabled {
		body["tools"] = req.tools
	} else {
		body["tools"] = []any{}
	}
	return body
}

func templateMessages(template map[string]any) []any {
	if messages, ok := template["messages"].([]any); ok {
		return messages
	}
	return nil
}

func modelConfigSource(envelope map[string]any) string {
	if config, ok := envelope["model_config"].(map[string]any); ok {
		if source, ok := config["source"].(string); ok && source != "" {
			return source
		}
	}
	return "system"
}

func deepCopyMap(input map[string]any) map[string]any {
	data, err := json.Marshal(input)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// ---------------------------------------------------------------------------
// Message conversion
// ---------------------------------------------------------------------------

func buildQoderMessages(templateMsgs, incoming []any, prompt string, toolsEnabled bool) []any {
	var rebuilt []any

	hasIncomingSystem := false
	for _, message := range incoming {
		if item, ok := message.(map[string]any); ok && stringValue(item["role"]) == "system" {
			hasIncomingSystem = true
			break
		}
	}
	if !hasIncomingSystem {
		for _, message := range templateMsgs {
			if item, ok := message.(map[string]any); ok && stringValue(item["role"]) == "system" {
				rebuilt = append(rebuilt, deepCopyMap(item))
			}
		}
	}

	for _, message := range incoming {
		item, ok := message.(map[string]any)
		if !ok {
			continue
		}
		// A single Claude-style user message may carry multiple tool_results.
		if stringValue(item["role"]) == "user" {
			if blocks, ok := item["content"].([]any); ok && len(blocks) > 0 {
				if first, ok := blocks[0].(map[string]any); ok && stringValue(first["type"]) == "tool_result" {
					for _, block := range blocks {
						part, ok := block.(map[string]any)
						if !ok || stringValue(part["type"]) != "tool_result" {
							continue
						}
						rebuilt = append(rebuilt, map[string]any{
							"role":         "tool",
							"tool_call_id": rawStringValue(part["tool_use_id"]),
							"content":      toolResultText(part["content"]),
						})
					}
					continue
				}
			}
		}
		if converted := convertIncomingMessage(item, toolsEnabled); converted != nil {
			rebuilt = append(rebuilt, converted)
		}
	}

	if len(rebuilt) == 0 && prompt != "" {
		rebuilt = append(rebuilt, buildUserMessage(prompt))
	}
	return rebuilt
}

func convertIncomingMessage(message map[string]any, toolsEnabled bool) map[string]any {
	role := stringValue(message["role"])
	content := message["content"]

	if blocks, ok := content.([]any); ok && len(blocks) > 0 {
		first, _ := blocks[0].(map[string]any)
		blockType := stringValue(first["type"])

		if role == "assistant" && hasBlockType(blocks, "tool_use") {
			out := map[string]any{"role": "assistant", "content": ""}
			var toolCalls []any
			var text string
			for _, block := range blocks {
				part, ok := block.(map[string]any)
				if !ok {
					continue
				}
				switch stringValue(part["type"]) {
				case "tool_use":
					arguments, _ := json.Marshal(part["input"])
					toolCalls = append(toolCalls, map[string]any{
						"id":   part["id"],
						"type": "function",
						"function": map[string]any{
							"name":      part["name"],
							"arguments": string(arguments),
						},
					})
				case "text":
					text += rawStringValue(part["text"])
				}
			}
			if text != "" {
				out["content"] = text
			}
			if len(toolCalls) > 0 {
				out["tool_calls"] = toolCalls
			}
			return out
		}

		if role == "user" && blockType == "tool_result" {
			var results []map[string]any
			for _, block := range blocks {
				part, ok := block.(map[string]any)
				if !ok || stringValue(part["type"]) != "tool_result" {
					continue
				}
				results = append(results, map[string]any{
					"role":         "tool",
					"tool_call_id": rawStringValue(part["tool_use_id"]),
					"content":      toolResultText(part["content"]),
				})
			}
			if len(results) > 0 {
				return results[0]
			}
			return nil
		}

		if blockType == "thinking" || blockType == "redacted_thinking" {
			var text string
			for _, block := range blocks {
				if part, ok := block.(map[string]any); ok && stringValue(part["type"]) == "text" {
					text += rawStringValue(part["text"])
				}
			}
			if text == "" {
				return nil
			}
			return buildStructuredMessage(role, text)
		}
	}

	text := normalizeMessageContent(message)
	if role == "assistant" && toolsEnabled {
		if toolCalls, ok := message["tool_calls"]; ok {
			out := buildStructuredMessage("assistant", text)
			out["tool_calls"] = toolCalls
			return out
		}
	}
	if role == "tool" {
		out := buildStructuredMessage("tool", text)
		if name := stringValue(message["name"]); name != "" {
			out["name"] = name
		}
		if id := stringValue(message["tool_call_id"]); id != "" {
			out["tool_call_id"] = id
		}
		return out
	}
	if text == "" {
		return nil
	}
	if role == "user" {
		return buildUserMessage(text)
	}
	return buildStructuredMessage(role, text)
}

func hasBlockType(blocks []any, blockType string) bool {
	for _, block := range blocks {
		if item, ok := block.(map[string]any); ok && stringValue(item["type"]) == blockType {
			return true
		}
	}
	return false
}

func toolResultText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		var out string
		for _, part := range value {
			if item, ok := part.(map[string]any); ok {
				out += rawStringValue(item["text"])
			}
		}
		return out
	}
	return ""
}

func buildUserMessage(text string) map[string]any {
	return map[string]any{
		"role":    "user",
		"content": "",
		"contents": []any{
			map[string]any{"type": "text", "text": text},
		},
		"response_meta":               blankResponseMeta(),
		"reasoning_content_signature": "",
	}
}

func buildStructuredMessage(role, text string) map[string]any {
	return map[string]any{
		"role":                        role,
		"content":                     text,
		"response_meta":               blankResponseMeta(),
		"reasoning_content_signature": "",
	}
}

func blankResponseMeta() map[string]any {
	return map[string]any{
		"id": "",
		"usage": map[string]any{
			"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0,
			"completion_tokens_details": map[string]any{"reasoning_tokens": 0},
			"prompt_tokens_details":     map[string]any{"cached_tokens": 0},
		},
	}
}

func normalizeMessageContent(message map[string]any) string {
	content := message["content"]
	if text, ok := content.(string); ok {
		return text
	}
	if content == nil {
		return ""
	}
	blocks, ok := content.([]any)
	if !ok {
		data, _ := json.Marshal(content)
		return string(data)
	}
	var out string
	for _, block := range blocks {
		if item, ok := block.(map[string]any); ok {
			if text := rawStringValue(item["text"]); text != "" {
				if out != "" {
					out += "\n\n"
				}
				out += text
			}
		}
	}
	return out
}

func extractLatestUserPrompt(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		if stringValue(message["role"]) == "user" {
			return normalizeMessageContent(message)
		}
	}
	return ""
}

// lastUserText returns the prompt of the last user turn after conversion. A
// converted user message stores its text in contents[0].text instead of
// content, so both shapes are checked.
func lastUserText(messages []any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message, _ := messages[i].(map[string]any)
		if stringValue(message["role"]) != "user" {
			continue
		}
		if text := normalizeMessageContent(message); text != "" {
			return text
		}
		if contents, ok := message["contents"].([]any); ok {
			for _, block := range contents {
				if item, ok := block.(map[string]any); ok {
					if text := rawStringValue(item["text"]); text != "" {
						return text
					}
				}
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Small JSON helpers
// ---------------------------------------------------------------------------

func stringValue(value any) string {
	return strings.TrimSpace(rawStringValue(value))
}

// rawStringValue preserves whitespace, which is significant in message
// content, reasoning deltas and tool-call argument fragments.
func rawStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%v", typed)
	}
	return ""
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return 0
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// hashFingerprint produces a non-reversible cache key component.
func hashFingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:16])
}
