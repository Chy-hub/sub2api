package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// maxRefreshTokenExpiresIn rejects absurd expiresIn values (10 years).
const maxRefreshTokenExpiresIn = int64(10 * 365 * 24 * 60 * 60)

// refreshTokenPayload is the refresh endpoint's token object.
type refreshTokenPayload struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"`
	Domain       string `json:"domain"`
}

// Refresh exchanges the refresh token for a new access token.
//
// X-Refresh-Token is sent to this endpoint only; chat requests must never carry
// it, since the chat upstream only needs the short-lived access token.
func (p *Provider) Refresh(ctx context.Context, acc *reverse.Account) (map[string]any, error) {
	if acc == nil {
		return nil, errors.New("workbuddy: account is required")
	}
	cred := newCredential(acc)
	if cred.refreshToken == "" {
		return nil, errors.New("workbuddy: refresh token is missing")
	}

	callCtx, cancel := withTimeout(ctx, refreshTimeout)
	defer cancel()

	endpoint := endpointsFor(cred.region).chatBase + tokenRefreshPath
	req, errReq := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, nil)
	if errReq != nil {
		return nil, fmt.Errorf("workbuddy build refresh request: %w", errReq)
	}
	applyCommonHeaders(req, cred)
	req.Header.Set("X-Refresh-Token", cred.refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")

	resp, errDo := p.client(acc).Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("workbuddy refresh request: %w", errDo)
	}
	defer func() { _ = resp.Body.Close() }()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, apiBodyLimit+1))
	if errRead != nil {
		return nil, fmt.Errorf("workbuddy read refresh response: %w", errRead)
	}
	if len(body) > apiBodyLimit {
		return nil, errors.New("workbuddy refresh response exceeds the size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamError(resp.StatusCode, string(body), resp.Header)
	}

	token, errToken := parseRefreshResponse(body)
	if errToken != nil {
		return nil, errToken
	}
	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return nil, errors.New("workbuddy: refresh response has no access token — re-login required")
	}

	updated := map[string]any{"access_token": accessToken}
	if refreshToken := strings.TrimSpace(token.RefreshToken); refreshToken != "" {
		updated["refresh_token"] = refreshToken
	}
	if token.ExpiresIn > 0 && token.ExpiresIn <= maxRefreshTokenExpiresIn {
		updated["expires_at"] = time.Now().Unix() + token.ExpiresIn
	}
	if domain := strings.TrimSpace(token.Domain); domain != "" {
		updated["domain"] = domain
	}
	return updated, nil
}

// parseRefreshResponse accepts both the enveloped and the direct response
// shapes:
//
//	{"code":0,"data":{"accessToken":...}}
//	{"accessToken":...}
func parseRefreshResponse(body []byte) (refreshTokenPayload, error) {
	var envelope struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &envelope); errUnmarshal == nil && len(envelope.Code) > 0 {
		code := strings.Trim(strings.TrimSpace(string(envelope.Code)), `"`)
		if code != "" && code != "0" && code != "null" {
			message := strings.TrimSpace(envelope.Msg)
			if message == "" {
				message = truncate(strings.TrimSpace(string(body)), 512)
			}
			return refreshTokenPayload{}, upstreamError(0, string(body), nil)
		}
		if len(envelope.Data) > 0 && strings.TrimSpace(string(envelope.Data)) != "null" {
			var token refreshTokenPayload
			if errToken := json.Unmarshal(envelope.Data, &token); errToken == nil {
				return token, nil
			}
		}
	}

	var token refreshTokenPayload
	if errUnmarshal := json.Unmarshal(body, &token); errUnmarshal == nil && token.AccessToken != "" {
		return token, nil
	}

	// Some deployments wrap the token object in a "data" key without an
	// envelope code.
	var wrapped struct {
		Data refreshTokenPayload `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &wrapped); errUnmarshal == nil && wrapped.Data.AccessToken != "" {
		return wrapped.Data, nil
	}
	return refreshTokenPayload{}, errors.New("workbuddy: refresh response has no access token — re-login required")
}
