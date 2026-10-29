package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// loginState is the opaque state carried between StartLogin and PollLogin.
type loginState struct {
	Account *reverse.Account
	Region  region
	State   string
}

// StartLogin begins the device-authorization flow: it obtains a state and the
// URL the user must open in a browser.
func (p *Provider) StartLogin(ctx context.Context, regionName string) (*reverse.LoginStart, error) {
	reg := normalizeRegion(regionName)
	endpoint := endpointsFor(reg).chatBase + authStatePath + "?platform=CLI"

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if errReq != nil {
		return nil, fmt.Errorf("workbuddy build login state request: %w", errReq)
	}
	applyLoginHeaders(req, reg)

	raw, errCall := doLoginCall(req)
	if errCall != nil {
		return nil, errCall
	}

	var payload struct {
		State string `json:"state"`
		URL   string `json:"url"`
		// Some deployments put the link under authUrl / loginUrl.
		AuthURL  string `json:"authUrl"`
		LoginURL string `json:"loginUrl"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy decode login state: %w", errUnmarshal)
	}
	state := strings.TrimSpace(payload.State)
	loginURL := firstNonEmpty(payload.URL, payload.AuthURL, payload.LoginURL)
	if state == "" || loginURL == "" {
		return nil, errors.New("workbuddy: login state response is incomplete")
	}

	return &reverse.LoginStart{
		URL:       loginURL,
		ExpiresAt: time.Now().Add(loginSessionTTL),
		State:     &loginState{Region: reg, State: state, Account: reverse.LoginAccount(ctx)},
	}, nil
}

// PollLogin advances the device-authorization flow: it polls the token endpoint
// and, once a token exists, fetches the account profile.
func (p *Provider) PollLogin(ctx context.Context, state any) (*reverse.LoginPollResult, error) {
	session, okSession := state.(*loginState)
	if session != nil && session.Account != nil {
		ctx = reverse.WithLoginAccount(ctx, session.Account)
	}
	if !okSession || session == nil || strings.TrimSpace(session.State) == "" {
		return nil, errors.New("workbuddy: invalid login state")
	}
	reg := session.Region
	if reg == "" {
		reg = regionCN
	}
	base := endpointsFor(reg).chatBase
	escapedState := url.QueryEscape(session.State)

	tokenURL := base + authTokenPath + "?state=" + escapedState
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)
	if errReq != nil {
		return nil, fmt.Errorf("workbuddy build login token request: %w", errReq)
	}
	applyLoginHeaders(req, reg)

	raw, errCall := doLoginCall(req)
	if errCall != nil {
		if isLoginPending(errCall) {
			return &reverse.LoginPollResult{Pending: true}, nil
		}
		return nil, errCall
	}

	var token struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if errUnmarshal := json.Unmarshal(raw, &token); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy decode login token: %w", errUnmarshal)
	}
	accessToken := strings.TrimSpace(token.AccessToken)
	refreshToken := strings.TrimSpace(token.RefreshToken)
	if accessToken == "" {
		return nil, errors.New("workbuddy: login token response has no access token")
	}

	accountURL := base + loginAccountPath + "?state=" + escapedState
	accountReq, errAccountReq := http.NewRequestWithContext(ctx, http.MethodGet, accountURL, nil)
	if errAccountReq != nil {
		return nil, fmt.Errorf("workbuddy build login account request: %w", errAccountReq)
	}
	applyLoginHeaders(accountReq, reg)
	accountReq.Header.Set("Authorization", "Bearer "+accessToken)

	accountRaw, errAccount := doLoginCall(accountReq)
	if errAccount != nil {
		if isLoginPending(errAccount) {
			return &reverse.LoginPollResult{Pending: true}, nil
		}
		return nil, errAccount
	}
	var account struct {
		UID          string `json:"uid"`
		Nickname     string `json:"nickname"`
		Email        string `json:"email"`
		Domain       string `json:"domain"`
		EnterpriseID string `json:"enterpriseId"`
	}
	if errUnmarshal := json.Unmarshal(accountRaw, &account); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy decode login account: %w", errUnmarshal)
	}

	profile := reverse.LoginProfile{
		UID:          strings.TrimSpace(account.UID),
		Nickname:     strings.TrimSpace(account.Nickname),
		Email:        strings.TrimSpace(account.Email),
		Domain:       strings.TrimSpace(account.Domain),
		EnterpriseID: strings.TrimSpace(account.EnterpriseID),
	}
	credentials := map[string]any{"access_token": accessToken}
	for key, value := range map[string]string{
		"refresh_token": refreshToken,
		"uid":           profile.UID,
		"nickname":      profile.Nickname,
		"domain":        profile.Domain,
		"enterprise_id": profile.EnterpriseID,
	} {
		if value != "" {
			credentials[key] = value
		}
	}
	return &reverse.LoginPollResult{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Profile:      profile,
		Credentials:  credentials,
	}, nil
}

// doLoginCall performs one login RPC. Login HTTP has no account scope, hence
// the empty proxy (environment proxy only).
func doLoginCall(req *http.Request) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(req.Context(), loginTimeout)
	defer cancel()
	return apiCall(reverse.HTTPClient(reverse.LoginProxyURL(ctx)), req.WithContext(ctx), apiBodyLimit)
}

// isLoginPending reports whether an error means "the user has not finished
// authorizing yet": HTTP 404, business code 11217, or the "login ing" text.
func isLoginPending(err error) bool {
	if err == nil {
		return false
	}
	var upstream *reverse.UpstreamError
	if errors.As(err, &upstream) {
		if upstream.StatusCode == http.StatusNotFound {
			return true
		}
		if strings.TrimSpace(upstream.Code) == "11217" {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"11217", "login ing", "pending", "not authorized", "waiting", "authorizing"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
