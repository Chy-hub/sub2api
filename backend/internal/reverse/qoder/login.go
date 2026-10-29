package qoder

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

const (
	loginFlowTTL     = 10 * time.Minute
	loginHTTPTimeout = 30 * time.Second
)

// loginState is the opaque handle kept by the gateway between StartLogin and
// PollLogin.
type loginState struct {
	account   *reverse.Account
	nonce     string
	verifier  string
	region    region
	expiresAt time.Time
}

func pkcePair() (verifier, challenge string, err error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buffer)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// StartLogin builds the Qoder PKCE device-authorization URL.
func (p *Provider) StartLogin(ctx context.Context, regionValue string) (*reverse.LoginStart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	verifier, challenge, err := pkcePair()
	if err != nil {
		return nil, err
	}
	nonce := randomHex(16)
	r := normalizeRegion(regionValue)
	params := url.Values{}
	params.Set("nonce", nonce)
	params.Set("challenge", challenge)
	params.Set("challenge_method", "S256")
	params.Set("client_id", oauthClientID)
	loginURL := endpointsFor(r).deviceLogin + "?" + params.Encode()
	expiresAt := time.Now().Add(loginFlowTTL)
	return &reverse.LoginStart{
		URL:       loginURL,
		ExpiresAt: expiresAt,
		State: &loginState{
			account:   reverse.LoginAccount(ctx),
			nonce:     nonce,
			verifier:  verifier,
			region:    r,
			expiresAt: expiresAt,
		},
	}, nil
}

// PollLogin performs one poll of the device-authorization endpoint. Upstream
// 404 means the user has not authorized yet.
func (p *Provider) PollLogin(ctx context.Context, state any) (*reverse.LoginPollResult, error) {
	pending, ok := state.(*loginState)
	if pending != nil && pending.account != nil {
		ctx = reverse.WithLoginAccount(ctx, pending.account)
	}
	if !ok || pending == nil {
		return nil, fmt.Errorf("qoder login state is missing or corrupt")
	}
	if time.Now().After(pending.expiresAt) {
		return nil, fmt.Errorf("qoder login session expired")
	}
	ep := endpointsFor(pending.region)
	token, refreshToken, waiting, err := pollDeviceToken(ctx, ep.poll, pending.nonce, pending.verifier)
	if err != nil {
		return nil, err
	}
	if waiting {
		return &reverse.LoginPollResult{Pending: true}, nil
	}

	client := reverse.HTTPClient(reverse.LoginProxyURL(ctx))
	profile := reverse.LoginProfile{}
	if userInfo, err := fetchUserInfo(ctx, client, ep.userinfo, token); err == nil {
		// The plugin's login profile reads userId specifically.
		profile.UID = firstNonEmpty(stringValue(userInfo["userId"]), stringValue(userInfo["id"]), stringValue(userInfo["uid"]))
		profile.Nickname = stringValue(userInfo["name"])
		profile.Email = strings.ToLower(stringValue(userInfo["email"]))
		profile.UserType = stringValue(userInfo["userType"])
		profile.EnterpriseID = stringValue(userInfo["organization_id"])
	}
	profile.Plan = fetchPlanName(ctx, client, ep.plan, token)

	credentials := map[string]any{
		"access_token": token,
		"region":       string(pending.region),
	}
	if refreshToken != "" {
		credentials["refresh_token"] = refreshToken
	}
	if profile.UID != "" {
		credentials["qoder_account_id"] = profile.UID
	}
	if profile.UserType != "" {
		credentials["qoder_user_type"] = profile.UserType
	}
	if profile.Email != "" {
		credentials["email"] = profile.Email
	}
	if profile.Nickname != "" {
		credentials["nickname"] = profile.Nickname
	}
	if profile.Plan != "" {
		credentials["plan"] = profile.Plan
	}
	return &reverse.LoginPollResult{
		Pending:      false,
		AccessToken:  token,
		RefreshToken: refreshToken,
		Profile:      profile,
		Credentials:  credentials,
	}, nil
}

func pollDeviceToken(ctx context.Context, endpoint, nonce, verifier string) (token, refreshToken string, waiting bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, loginHTTPTimeout)
	defer cancel()
	pollURL := fmt.Sprintf("%s?nonce=%s&verifier=%s&challenge_method=S256",
		endpoint, url.QueryEscape(nonce), url.QueryEscape(verifier))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return "", "", false, err
	}
	response, err := reverse.HTTPClient(reverse.LoginProxyURL(ctx)).Do(request)
	if err != nil {
		return "", "", false, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := readBodyLimited(response.Body)
	if err != nil {
		return "", "", false, fmt.Errorf("read qoder poll response: %w", err)
	}
	switch response.StatusCode {
	case http.StatusNotFound:
		return "", "", true, nil
	case http.StatusOK:
	default:
		return "", "", false, upstreamError(response.StatusCode, response.Header, body)
	}
	parsed, err := decodeJSONResponse(body)
	if err != nil {
		return "", "", false, fmt.Errorf("decode qoder poll response: %w", err)
	}
	deviceToken := stringValue(parsed["token"])
	if deviceToken == "" {
		return "", "", false, fmt.Errorf("qoder poll response has no token")
	}
	return deviceToken, stringValue(parsed["refresh_token"]), false, nil
}
