package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// tokenType classifies Qoder credential strings by their prefix.
type tokenType int

const (
	tokenUnknown tokenType = iota
	tokenDevice
	tokenPersonal
	tokenRefresh
)

func detectTokenType(token string) tokenType {
	switch {
	case strings.HasPrefix(token, "dt-"):
		return tokenDevice
	case strings.HasPrefix(token, "pt-"):
		return tokenPersonal
	case strings.HasPrefix(token, "drt-"):
		return tokenRefresh
	}
	return tokenUnknown
}

// bridge is a cached cosy session plus the parsed request template. Creating
// one costs a userinfo call or a jobToken exchange plus RSA/AES work, so it is
// reused for the lifetime of the cache entry.
type bridge struct {
	session  *sessionContext
	region   region
	template map[string]any
}

func accountAccessToken(acc *reverse.Account) string {
	return firstNonEmpty(
		acc.CredentialString("access_token"),
		acc.CredentialString("device_token"),
		acc.CredentialString("token"),
	)
}

func credentialFingerprint(acc *reverse.Account) string {
	if acc == nil {
		return ""
	}
	return hashFingerprint(
		accountAccessToken(acc),
		acc.CredentialString("refresh_token"),
		acc.Region,
		acc.CredentialString("uid"),
		acc.CredentialString("qoder_account_id"),
		acc.CredentialString("user_type"),
		acc.CredentialString("qoder_user_type"),
		acc.CredentialString("machine_id"),
		acc.CredentialString("machine_token"),
		acc.CredentialString("machine_type"),
	)
}

func newBridge(ctx context.Context, acc *reverse.Account, template map[string]any) (*bridge, error) {
	token := accountAccessToken(acc)
	if token == "" {
		return nil, reverse.NewUpstreamError(http.StatusUnauthorized, "missing_credential", "qoder account has no access_token")
	}
	refreshToken := acc.CredentialString("refresh_token")
	r := normalizeRegion(acc.Region)
	ep := endpointsFor(r)
	client := reverse.HTTPClient(acc.ProxyURL)

	credentialUID := firstNonEmpty(acc.CredentialString("uid"), acc.CredentialString("qoder_account_id"))
	credentialUserType := firstNonEmpty(acc.CredentialString("user_type"), acc.CredentialString("qoder_user_type"))

	var identity authIdentity
	if detectTokenType(token) == tokenDevice {
		userInfo, err := fetchUserInfo(ctx, client, ep.userinfo, token)
		if err != nil {
			return nil, err
		}
		uid := firstNonEmpty(credentialUID, resolveUserID(userInfo))
		identity = authIdentity{
			Name:               stringValue(userInfo["name"]),
			Aid:                uid,
			Uid:                uid,
			OrganizationID:     stringValue(userInfo["organization_id"]),
			OrganizationName:   stringValue(userInfo["organization_name"]),
			UserType:           firstNonEmpty(credentialUserType, stringValue(userInfo["userType"]), "personal_standard"),
			SecurityOauthToken: token,
			RefreshToken:       refreshToken,
		}
	} else {
		// The jobToken exchange happens before the uid is known; the plugin
		// derives a stable identity from the credential seed for this call.
		exchangeID, exchangeToken, exchangeType := machineIdentity(acc, "cred:"+token)
		jobToken, err := exchangeJobToken(ctx, client, ep.jobToken, token, exchangeID, exchangeToken, exchangeType)
		if err != nil {
			return nil, err
		}
		uid := firstNonEmpty(credentialUID, stringValue(jobToken["id"]))
		identity = authIdentity{
			Name:               stringValue(jobToken["name"]),
			Aid:                uid,
			Uid:                uid,
			UserType:           firstNonEmpty(credentialUserType, stringValue(jobToken["userType"]), "personal_standard"),
			SecurityOauthToken: stringValue(jobToken["securityOauthToken"]),
			RefreshToken:       firstNonEmpty(stringValue(jobToken["refreshToken"]), refreshToken),
		}
		if identity.SecurityOauthToken == "" {
			return nil, reverse.NewUpstreamError(http.StatusUnauthorized, "credential_invalid", "jobToken response has no securityOauthToken")
		}
	}

	seed := identity.Uid
	if seed == "" {
		seed = "cred:" + token
	}
	machineID, machineToken, machineType := machineIdentity(acc, seed)
	session, err := newSession(identity, machineID, machineToken, machineType)
	if err != nil {
		return nil, fmt.Errorf("create qoder cosy session: %w", err)
	}
	return &bridge{session: session, region: r, template: template}, nil
}

// machineIdentity returns the per-account overrides when present, otherwise
// the stable derivation used by desktop chat requests. Check-in deliberately
// does not use this helper (see checkin.go).
func machineIdentity(acc *reverse.Account, seed string) (id, token, machineType string) {
	id = acc.CredentialString("machine_id")
	token = acc.CredentialString("machine_token")
	machineType = acc.CredentialString("machine_type")
	if id == "" {
		id = deriveMachineID(seed)
	}
	if token == "" {
		token = deriveMachineToken(seed)
	}
	if machineType == "" {
		machineType = deriveMachineType(seed)
	}
	return id, token, machineType
}

func resolveUserID(userInfo map[string]any) string {
	return firstNonEmpty(stringValue(userInfo["id"]), stringValue(userInfo["userId"]), stringValue(userInfo["uid"]))
}

// fetchUserInfo validates a device token and reads the account identity.
func fetchUserInfo(ctx context.Context, client *http.Client, endpoint, token string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readBodyLimited(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read qoder userinfo: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, upstreamError(response.StatusCode, response.Header, body, token)
	}
	parsed, err := decodeJSONResponse(body)
	if err != nil {
		return nil, fmt.Errorf("decode qoder userinfo: %w", err)
	}
	return parsed, nil
}

func fetchPlanName(ctx context.Context, client *http.Client, endpoint, token string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readBodyLimited(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		return ""
	}
	parsed, err := decodeJSONResponse(body)
	if err != nil {
		return ""
	}
	return stringValue(parsed["plan_tier_name"])
}

// exchangeJobToken converts a PAT (pt-) or refresh token (drt-) into a cosy
// session token. The response is plain JSON (or custom base64 on some gateways).
func exchangeJobToken(ctx context.Context, client *http.Client, endpoint, token, machineID, machineToken, machineType string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if strings.TrimSpace(endpoint) == "" {
		endpoint = endpointsGlobal.jobToken
	}
	personalToken := token
	refreshToken := ""
	if detectTokenType(token) == tokenRefresh {
		personalToken = ""
		refreshToken = token
	}
	inner, _ := json.Marshal(map[string]any{
		"personalToken":      personalToken,
		"securityOauthToken": "",
		"refreshToken":       refreshToken,
		"needRefresh":        refreshToken != "",
		"authInfo":           map[string]any{},
	})
	outer, _ := json.Marshal(map[string]any{
		"payload":       string(inner),
		"encodeVersion": "1",
	})
	body, err := encodeCustomBase64(outer)
	if err != nil {
		return nil, err
	}

	date := currentDate()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("cosy-machinetoken", machineToken)
	request.Header.Set("cosy-machinetype", machineType)
	request.Header.Set("login-version", "v2")
	request.Header.Set("appcode", appCode)
	request.Header.Set("accept", "application/json")
	request.Header.Set("accept-encoding", "identity")
	request.Header.Set("cosy-version", qoderVersion)
	request.Header.Set("cosy-clienttype", "5")
	request.Header.Set("date", date)
	request.Header.Set("signature", signLegacy(date))
	request.Header.Set("content-type", "application/json")
	request.Header.Set("cosy-machineid", machineID)
	request.Header.Set("user-agent", "Go-http-client/2.0")

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := readBodyLimited(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read qoder jobToken response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, upstreamError(response.StatusCode, response.Header, raw, token, machineToken)
	}
	parsed, err := decodeJSONResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("decode qoder jobToken response: %w", err)
	}
	return parsed, nil
}

// signedGet performs a cosy-signed GET (model catalog, quota).
func (b *bridge) signedGet(ctx context.Context, client *http.Client, endpoint string) (map[string]any, error) {
	pathSig, err := pathSignature(endpoint)
	if err != nil {
		return nil, err
	}
	headers, err := buildHeaders(b.session, pathSig, "", "application/json", nil)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readBodyLimited(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read qoder response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, upstreamError(response.StatusCode, response.Header, body, b.session.MachineToken, b.session.Identity.SecurityOauthToken)
	}
	return decodeJSONResponse(body)
}

// refreshCredentials implements the plugin's RefreshCredential: device tokens
// are only validated via userinfo; PAT/refresh tokens go through jobToken.
func refreshCredentials(ctx context.Context, acc *reverse.Account) (map[string]any, error) {
	token := accountAccessToken(acc)
	if token == "" {
		return nil, reverse.NewUpstreamError(http.StatusUnauthorized, "missing_credential", "qoder account has no access_token")
	}
	refreshToken := acc.CredentialString("refresh_token")
	r := normalizeRegion(acc.Region)
	ep := endpointsFor(r)
	client := reverse.HTTPClient(acc.ProxyURL)

	if detectTokenType(token) == tokenDevice {
		if _, err := fetchUserInfo(ctx, client, ep.userinfo, token); err != nil {
			return nil, err
		}
		return map[string]any{"access_token": token, "refresh_token": refreshToken}, nil
	}

	machineID, machineToken, machineType := machineIdentity(acc, "cred:"+token)
	jobToken, err := exchangeJobToken(ctx, client, ep.jobToken, token, machineID, machineToken, machineType)
	if err != nil {
		return nil, err
	}
	nextToken := stringValue(jobToken["securityOauthToken"])
	if nextToken == "" {
		return nil, reverse.NewUpstreamError(http.StatusUnauthorized, "credential_invalid", "jobToken response has no securityOauthToken")
	}
	nextRefresh := firstNonEmpty(stringValue(jobToken["refreshToken"]), refreshToken)
	return map[string]any{"access_token": nextToken, "refresh_token": nextRefresh}, nil
}
