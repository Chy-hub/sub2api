package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

const (
	reverseLoginSessionTTL   = 15 * time.Minute
	reverseLoginQoderTTL     = 10 * time.Minute
	reverseLoginPollInterval = 2 * time.Second
	reverseLoginTicketTTL    = 5 * time.Minute
	reverseUpstreamTimeout   = 15 * time.Minute
	// Streaming generations can legitimately run far longer than a short RPC.
	// The stream is still bounded so an idle upstream cannot pin a connection
	// forever; callers additionally cancel it when the client disconnects.
	reverseStreamTimeout = 60 * time.Minute
)

type reverseLoginSession struct {
	ID          string
	Platform    string
	Provider    string
	Region      string
	AccountID   int64
	ActorUserID int64
	State       any
	ExpiresAt   time.Time
	LastPoll    time.Time
	Polling     bool
}

type reverseLoginTicket struct {
	Platform    string
	Credentials map[string]any
	ActorUserID int64
	ExpiresAt   time.Time
}

// ReverseLoginStart is returned when a browser login starts.
type ReverseLoginStart struct {
	SessionID string    `json:"session_id"`
	LoginURL  string    `json:"login_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ReverseLoginPoll is one browser login poll outcome.
type ReverseLoginPoll struct {
	Pending     bool                 `json:"pending"`
	LoginTicket string               `json:"login_ticket,omitempty"`
	Profile     reverse.LoginProfile `json:"profile,omitempty"`
	Account     *Account             `json:"account,omitempty"`
}

// ReverseGateway adapts reverse providers to the OpenAI gateway.
type ReverseGateway struct {
	accountRepo AccountRepository
	registry    *reverse.Registry

	mu       sync.Mutex
	sessions map[string]*reverseLoginSession
	tickets  map[string]*reverseLoginTicket

	refreshLocks   sync.Map // account id -> *sync.Mutex
	operationLocks sync.Map
	lockCache      LeaderLockCache
	lockDB         *sql.DB

	// Per-request upstream bounds; zero falls back to the package defaults.
	upstreamTimeout       time.Duration
	streamUpstreamTimeout time.Duration
}

// NewReverseGateway builds the gateway and registers the built-in providers.
func NewReverseGateway(accountRepo AccountRepository) *ReverseGateway {
	registry := reverse.NewRegistry()
	registerReverseProviders(registry)
	return &ReverseGateway{
		accountRepo: accountRepo,
		registry:    registry,
		sessions:    map[string]*reverseLoginSession{},
		tickets:     map[string]*reverseLoginTicket{},

		upstreamTimeout:       reverseUpstreamTimeout,
		streamUpstreamTimeout: reverseStreamTimeout,
	}
}

func reverseRandomID() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (g *ReverseGateway) providerName(account *Account) (string, string, bool) {
	if account == nil {
		return "", "", false
	}
	return domain.ReverseProviderOf(account.Platform)
}

func (g *ReverseGateway) reverseAccount(account *Account) (*reverse.Account, string, error) {
	provider, region, ok := domain.ReverseProviderOf(account.Platform)
	if !ok {
		return nil, "", fmt.Errorf("platform %s is not a reverse platform", account.Platform)
	}
	return &reverse.Account{
		ID:          account.ID,
		Platform:    account.Platform,
		Provider:    provider,
		Region:      region,
		Credentials: account.Credentials,
		Extra:       account.Extra,
		ProxyURL:    resolveAccountProxyURL(account),
	}, provider, nil
}

func (g *ReverseGateway) providerFor(account *Account) (reverse.Provider, *reverse.Account, error) {
	acc, name, err := g.reverseAccount(account)
	if err != nil {
		return nil, nil, err
	}
	provider, ok := g.registry.Get(name)
	if !ok {
		return nil, nil, fmt.Errorf("reverse provider %s is not registered", name)
	}
	return provider, acc, nil
}

// Forward sends a Chat Completions request through the reverse provider.
func (g *ReverseGateway) Forward(ctx context.Context, account *Account, body []byte, stream bool) (*http.Response, error) {
	if g == nil {
		return nil, errors.New("reverse gateway is not configured")
	}
	provider, acc, err := g.providerFor(account)
	if err != nil {
		return nil, err
	}
	timeout := g.upstreamTimeout
	if timeout <= 0 {
		timeout = reverseUpstreamTimeout
	}
	if stream {
		timeout = g.streamUpstreamTimeout
		if timeout <= 0 {
			timeout = reverseStreamTimeout
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)

	resp, forwardErr := provider.Forward(callCtx, acc, body, stream)
	if forwardErr == nil {
		resp.Body = &reverseCancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}
	var upstreamErr *reverse.UpstreamError
	if !errors.As(forwardErr, &upstreamErr) {
		cancel()
		return nil, forwardErr
	}
	if upstreamErr.StatusCode == http.StatusUnauthorized && g.refreshAccount(ctx, account) == nil {
		provider, acc, err = g.providerFor(account)
		if err == nil {
			resp, forwardErr = provider.Forward(callCtx, acc, body, stream)
			if forwardErr == nil {
				resp.Body = &reverseCancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
				return resp, nil
			}
			if !errors.As(forwardErr, &upstreamErr) {
				cancel()
				return nil, forwardErr
			}
		}
	}
	cancel()
	if upstreamErr.Kind == string(reverse.FailureKindModelBlocked) || upstreamErr.Code == "6004" {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Model != "" && g.accountRepo != nil {
			wait := upstreamErr.RetryAfter
			if wait <= 0 {
				wait = 5 * time.Minute
			}
			if upstreamErr.Kind == string(reverse.FailureKindModelBlocked) {
				wait = time.Hour
			}
			if err := g.accountRepo.SetModelRateLimit(ctx, account.ID, payload.Model, time.Now().Add(wait), "reverse "+upstreamErr.Code); err != nil {
				logger.LegacyPrintf("service.reverse", "persist reverse model cooldown failed: account=%d err=%v", account.ID, err)
			}
		}
		// Route through the gateway's existing account-specific model failover;
		// the rest of this account's models remain available.
		originalCode := upstreamErr.Code
		upstreamErr.StatusCode = http.StatusBadRequest
		upstreamErr.Code = "model_not_found"
		if originalCode == "6004" {
			upstreamErr.Code = "model_rate_limited"
		}
		upstreamErr.Message = originalCode + ": " + upstreamErr.Message
	}
	return reverseErrorResponse(upstreamErr), nil
}

type reverseCancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *reverseCancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	if c.cancel != nil {
		c.cancel()
	}
	return err
}

func reverseErrorResponse(upstreamErr *reverse.UpstreamError) *http.Response {
	status := upstreamErr.StatusCode
	if status < 400 {
		status = http.StatusBadGateway
	}
	payload := map[string]any{
		"error": map[string]any{
			"message": upstreamErr.Message,
			"type":    "upstream_error",
			"code":    upstreamErr.Code,
		},
	}
	body, _ := json.Marshal(payload)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	if upstreamErr.RetryAfter > 0 {
		header.Set("Retry-After", fmt.Sprintf("%d", (upstreamErr.RetryAfter+time.Second-1)/time.Second))
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func (g *ReverseGateway) refreshLock(accountID int64) *sync.Mutex {
	value, _ := g.refreshLocks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (g *ReverseGateway) refreshAccount(ctx context.Context, account *Account) error {
	if account == nil || g.accountRepo == nil {
		return errors.New("reverse account is unavailable")
	}
	lock := g.refreshLock(account.ID)
	lock.Lock()
	defer lock.Unlock()
	latest, err := g.accountRepo.GetByID(ctx, account.ID)
	if err != nil {
		return err
	}
	if latest != nil {
		oldToken := account.GetCredential("access_token")
		account.Credentials = latest.Credentials
		if token := latest.GetCredential("access_token"); token != "" && token != oldToken {
			return nil
		}
	}

	provider, acc, err := g.providerFor(account)
	if err != nil {
		return err
	}
	updated, err := provider.Refresh(ctx, acc)
	if err != nil {
		return err
	}
	merged := mergeReverseCredentials(account.Credentials, updated)
	if err := persistAccountCredentials(ctx, g.accountRepo, account, merged); err != nil {
		return err
	}
	return nil
}

// Refresh refreshes reverse credentials and returns the merged credential map.
func (g *ReverseGateway) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if err := g.refreshAccount(ctx, account); err != nil {
		return nil, err
	}
	return account.Credentials, nil
}

func mergeReverseCredentials(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	for key, value := range existing {
		out[key] = value
	}
	for key, value := range incoming {
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

// Checkin performs a daily check-in and records the result in extra.
func (g *ReverseGateway) Checkin(ctx context.Context, account *Account) (*reverse.CheckinResult, map[string]any, error) {
	unlock, err := g.lockOperation(ctx, account.ID)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	provider, acc, err := g.providerFor(account)
	if err != nil {
		return nil, nil, err
	}
	result, checkinErr := provider.Checkin(ctx, acc)
	var upstream *reverse.UpstreamError
	if errors.As(checkinErr, &upstream) && upstream.StatusCode == http.StatusUnauthorized && g.refreshAccount(ctx, account) == nil {
		provider, acc, err = g.providerFor(account)
		if err == nil {
			result, checkinErr = provider.Checkin(ctx, acc)
		}
	}
	state := map[string]any{
		"last_attempt_at": time.Now().UTC().Format(time.RFC3339),
	}
	if old, ok := account.Extra[ReverseCheckinExtraKey].(map[string]any); ok {
		if last, exists := old["last_success_at"]; exists {
			state["last_success_at"] = last
		}
	}
	if result != nil {
		state["status"] = "failed"
		if result.Success && checkinErr == nil {
			state["status"] = "success"
			state["last_success_at"] = time.Now().UTC().Format(time.RFC3339)
		}
		state["message"] = result.Message
		state["reward"] = result.Reward
		state["date"] = result.Date
		if result.Details != nil {
			state["details"] = result.Details
		}
	} else {
		state["status"] = "failed"
		if checkinErr != nil {
			state["message"] = checkinErr.Error()
		}
	}
	storeCtx, storeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer storeCancel()
	if storeErr := g.storeReverseExtra(storeCtx, account, ReverseCheckinExtraKey, state); storeErr != nil {
		logger.LegacyPrintf("service.reverse", "reverse checkin state update failed: account=%d err=%v", account.ID, storeErr)
		if checkinErr == nil {
			checkinErr = storeErr
		}
	}
	return result, state, checkinErr
}

// ListModels returns the upstream model catalog and caches it in extra.
func (g *ReverseGateway) ListModels(ctx context.Context, account *Account) ([]reverse.Model, error) {
	provider, acc, err := g.providerFor(account)
	if err != nil {
		return nil, err
	}
	models, err := provider.ListModels(ctx, acc)
	if err != nil {
		return nil, err
	}
	if g.accountRepo != nil && account.ID > 0 {
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		snapshot := map[string]any{
			"updated_at": time.Now().UTC().Format(time.RFC3339),
			"models":     ids,
			"catalog":    models,
		}
		if err := g.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{ReverseModelCatalogExtraKey: snapshot}); err != nil {
			logger.LegacyPrintf("service.reverse", "reverse model catalog update failed: account=%d err=%v", account.ID, err)
		}
	}
	return models, nil
}

// Test performs a short non-stream request.
func (g *ReverseGateway) Test(ctx context.Context, account *Account, model, prompt string) (*reverse.TestResult, error) {
	provider, acc, err := g.providerFor(account)
	if err != nil {
		return nil, err
	}
	return provider.Test(ctx, acc, account.GetMappedModel(model), prompt)
}

// StartLogin begins a browser login session for a platform or account.
// The session is bound to the requesting administrator so another admin
// cannot poll it even if the opaque ID leaks.
func (g *ReverseGateway) StartLogin(ctx context.Context, platform string, accountID, actorUserID int64, options ...*reverse.Account) (*ReverseLoginStart, error) {
	providerName, region, ok := domain.ReverseProviderOf(platform)
	if !ok {
		return nil, fmt.Errorf("platform %s is not a reverse platform", platform)
	}
	if accountID > 0 && g.accountRepo != nil {
		account, err := g.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if account.Platform != platform {
			return nil, ErrReverseLoginPlatformMismatch
		}
		acc, _, err := g.reverseAccount(account)
		if err != nil {
			return nil, err
		}
		ctx = reverse.WithLoginAccount(ctx, acc)
	} else if len(options) > 0 && options[0] != nil {
		ctx = reverse.WithLoginAccount(ctx, options[0])
	}
	login, ok := g.registry.Login(providerName)
	if !ok {
		return nil, fmt.Errorf("reverse provider %s does not support browser login", providerName)
	}
	start, err := login.StartLogin(ctx, region)
	if err != nil {
		return nil, err
	}
	ttl := reverseLoginSessionTTL
	if providerName == "qoder" {
		ttl = reverseLoginQoderTTL
	}
	if !start.ExpiresAt.IsZero() {
		ttl = time.Until(start.ExpiresAt)
		if ttl < time.Minute {
			ttl = time.Minute
		}
	}
	session := &reverseLoginSession{
		ID:          reverseRandomID(),
		Platform:    platform,
		Provider:    providerName,
		Region:      region,
		AccountID:   accountID,
		ActorUserID: actorUserID,
		State:       start.State,
		ExpiresAt:   time.Now().Add(ttl),
	}
	g.mu.Lock()
	g.sessions[session.ID] = session
	g.mu.Unlock()
	return &ReverseLoginStart{SessionID: session.ID, LoginURL: start.URL, ExpiresAt: session.ExpiresAt}, nil
}

// PollLogin advances a browser login session owned by the requesting admin.
func (g *ReverseGateway) PollLogin(ctx context.Context, sessionID string, actorUserID int64) (*ReverseLoginPoll, error) {
	now := time.Now()
	g.mu.Lock()
	session := g.sessions[sessionID]
	if session == nil || now.After(session.ExpiresAt) {
		delete(g.sessions, sessionID)
		g.mu.Unlock()
		return nil, errors.New("login session expired or not found")
	}
	// A mismatched owner must not be able to consume or delete another admin's
	// session, so report the same error without touching the session.
	if session.ActorUserID != actorUserID {
		g.mu.Unlock()
		return nil, errors.New("login session expired or not found")
	}
	if session.Polling || now.Sub(session.LastPoll) < reverseLoginPollInterval {
		g.mu.Unlock()
		return &ReverseLoginPoll{Pending: true}, nil
	}
	session.LastPoll = now
	session.Polling = true
	g.mu.Unlock()
	defer func() { g.mu.Lock(); session.Polling = false; g.mu.Unlock() }()

	login, ok := g.registry.Login(session.Provider)
	if !ok {
		return nil, fmt.Errorf("reverse provider %s does not support browser login", session.Provider)
	}
	result, err := login.PollLogin(ctx, session.State)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Pending {
		return &ReverseLoginPoll{Pending: true}, nil
	}
	credentials := result.Credentials
	if credentials == nil {
		credentials = map[string]any{}
	}
	if result.AccessToken != "" {
		credentials["access_token"] = result.AccessToken
	}
	if result.RefreshToken != "" {
		credentials["refresh_token"] = result.RefreshToken
	}
	if profile := result.Profile; profile.UID != "" || profile.Nickname != "" || profile.Email != "" {
		for key, value := range map[string]string{
			"uid": profile.UID, "nickname": profile.Nickname, "email": profile.Email,
			"domain": profile.Domain, "enterprise_id": profile.EnterpriseID,
			"qoder_user_type": profile.UserType, "plan": profile.Plan,
		} {
			if strings.TrimSpace(value) != "" {
				credentials[key] = value
			}
		}
	}

	if session.AccountID > 0 {
		if g.accountRepo == nil {
			return nil, errors.New("account repository is not configured")
		}
		account, err := g.accountRepo.GetByID(ctx, session.AccountID)
		if err != nil {
			return nil, err
		}
		if account.Platform != session.Platform {
			return nil, errors.New("login platform does not match account platform")
		}
		merged := mergeReverseCredentials(account.Credentials, credentials)
		if err := persistAccountCredentials(ctx, g.accountRepo, account, merged); err != nil {
			return nil, err
		}
		g.mu.Lock()
		delete(g.sessions, sessionID)
		g.mu.Unlock()
		return &ReverseLoginPoll{Profile: result.Profile, Account: account}, nil
	}

	ticket := reverseRandomID()
	g.mu.Lock()
	delete(g.sessions, sessionID)
	g.tickets[ticket] = &reverseLoginTicket{
		Platform:    session.Platform,
		Credentials: credentials,
		ActorUserID: session.ActorUserID,
		ExpiresAt:   time.Now().Add(reverseLoginTicketTTL),
	}
	g.mu.Unlock()
	return &ReverseLoginPoll{LoginTicket: ticket, Profile: result.Profile}, nil
}

// PreviewAccount resolves browser-login credentials server-side without
// consuming the ticket or exposing tokens to the create-account page. The
// ticket must belong to the requesting administrator.
func (g *ReverseGateway) PreviewAccount(platform string, credentials map[string]any, ticket string, actorUserID int64) (*Account, error) {
	if !IsReversePlatform(platform) {
		return nil, ErrReverseLoginPlatformMismatch
	}
	merged := make(map[string]any, len(credentials))
	for key, value := range credentials {
		merged[key] = value
	}
	if ticket = strings.TrimSpace(ticket); ticket != "" {
		g.mu.Lock()
		entry := g.tickets[ticket]
		if entry == nil || entry.ActorUserID != actorUserID || time.Now().After(entry.ExpiresAt) {
			g.mu.Unlock()
			return nil, ErrReverseLoginTicketInvalid
		}
		if entry.Platform != platform {
			g.mu.Unlock()
			return nil, ErrReverseLoginPlatformMismatch
		}
		for key, value := range entry.Credentials {
			merged[key] = value
		}
		g.mu.Unlock()
	}
	normalized, err := normalizeReverseCredentials(platform, merged)
	if err != nil {
		return nil, err
	}
	if token, _ := normalized["access_token"].(string); strings.TrimSpace(token) == "" {
		return nil, errors.New("access_token or a valid reverse login ticket is required")
	}
	return &Account{Platform: platform, Type: AccountTypeReverse, Credentials: normalized}, nil
}

// AcquireLoginTicket reserves a one-time ticket while an account is created.
// finish(false) returns an unexpired ticket so validation and database errors
// do not force the administrator to repeat the browser login. Only the admin
// who completed the browser login may redeem the ticket.
func (g *ReverseGateway) AcquireLoginTicket(ticket string, actorUserID int64) (string, map[string]any, func(bool), bool) {
	ticket = strings.TrimSpace(ticket)
	g.mu.Lock()
	defer g.mu.Unlock()
	entry := g.tickets[ticket]
	if entry == nil {
		return "", nil, nil, false
	}
	if time.Now().After(entry.ExpiresAt) {
		delete(g.tickets, ticket)
		return "", nil, nil, false
	}
	// A mismatched owner must not be able to consume or invalidate another
	// administrator's ticket.
	if entry.ActorUserID != actorUserID {
		return "", nil, nil, false
	}
	delete(g.tickets, ticket)
	credentials := make(map[string]any, len(entry.Credentials))
	for key, value := range entry.Credentials {
		credentials[key] = value
	}
	var once sync.Once
	finish := func(created bool) {
		once.Do(func() {
			if created {
				return
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if time.Now().Before(entry.ExpiresAt) {
				g.tickets[ticket] = entry
			}
		})
	}
	return entry.Platform, credentials, finish, true
}

// CleanupLoginSessions drops expired login sessions and tickets, and lets the
// providers evict stale per-account caches.
func (g *ReverseGateway) CleanupLoginSessions(now time.Time) {
	if g == nil {
		return
	}
	g.registry.CleanupCaches(now)
	g.mu.Lock()
	defer g.mu.Unlock()
	for id, session := range g.sessions {
		if now.After(session.ExpiresAt) {
			delete(g.sessions, id)
		}
	}
	for ticket, entry := range g.tickets {
		if now.After(entry.ExpiresAt) {
			delete(g.tickets, ticket)
		}
	}
}
