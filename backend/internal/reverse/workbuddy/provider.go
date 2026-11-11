// Package workbuddy adapts the WorkBuddy (CodeBuddy) reverse-proxy protocol to
// the project's reverse.Provider boundary.
//
// The package is a compact port of the WorkBuddy reference client: it reuses
// the same endpoints, request-header matrix, request-body normalization and SSE
// shaping, fingerprint sanitization and account operations. Host configuration
// and persistence are provided by sub2api's account services.
package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// Upstream paths. Kept together because a region switch must move them as a
// group; scattering URL construction is how half-switched deployments happen.
const (
	chatCompletionsPath = "/v2/chat/completions"

	authStatePath    = "/v2/plugin/auth/state"
	authTokenPath    = "/v2/plugin/auth/token"
	loginAccountPath = "/v2/plugin/login/account"
	tokenRefreshPath = "/v2/plugin/auth/token/refresh"

	v3ConfigPath               = "/v3/config"
	enterpriseModelsPathCN     = "/console/enterprises/personal/models"
	enterpriseModelsPathGlobal = "/v2/enterprises/personal/models"

	dailyCheckinPath  = "/v2/billing/meter/daily-checkin"
	growthHeatmapPath = "/activity/growth/heatmap"
	reportPath        = "/v2/report"
)

// Outbound identity shared by both deployments.
//
// The versions below are the CN defaults, kept as package constants because the
// desktop-fingerprint event families use them and those exist only on CN. The
// chat/UA path reads the per-region values from endpointsFor instead: the two
// deployments ship different genuine clients (CN 5.8.3 / CLI 2.179.0, Global
// 5.8.4 / CLI 2.161.0) and the upstream fingerprints the UA against them.
const (
	clientVersion = "5.8.3"
	cliVersion    = "2.179.0"
	clientName    = "WorkBuddy"

	codeBuddyIDEUA = "CodeBuddyIDE/4.12.0 CodeBuddy/4.12.0"
	loginClientUA  = "CLI/2.63.2 CodeBuddy/2.63.2"
)

const (
	shortCallTimeout = 60 * time.Second
	loginTimeout     = 30 * time.Second
	refreshTimeout   = 30 * time.Second

	apiBodyLimit    = 1 << 20
	configBodyLimit = 2 << 20

	modelCacheTTL = 10 * time.Minute

	// degradedPromptTTL bounds how long a content-policy downgrade sticks. The
	// marker exists to avoid retrying a rejected prompt forever; after a day a
	// fresh attempt is reasonable, and the entry stops pinning account state.
	degradedPromptTTL = 24 * time.Hour

	loginSessionTTL = 15 * time.Minute
)

// region is one WorkBuddy deployment. CN and Global are independent: domains,
// model catalogs and tokens do not cross over.
type region string

const (
	regionCN     region = "cn"
	regionGlobal region = "global"
)

func normalizeRegion(raw string) region {
	if strings.EqualFold(strings.TrimSpace(raw), string(regionGlobal)) {
		return regionGlobal
	}
	return regionCN
}

func (r region) isGlobal() bool { return r == regionGlobal }

// endpoints is the full endpoint table for one region.
//
// The client and CLI versions live here rather than in package constants
// because the two deployments ship different genuine clients, and the upstream
// fingerprints the UA against them: CN reports 5.8.3 / 2.179.0, Global 5.8.4 /
// 2.161.0.
type endpoints struct {
	chatBase       string
	billingBase    string
	origin         string
	acceptLanguage string
	platformLabel  string
	clientVersion  string
	cliVersion     string
}

func endpointsFor(r region) endpoints {
	if r.isGlobal() {
		return endpoints{
			chatBase:       "https://www.workbuddy.ai",
			billingBase:    "https://www.workbuddy.ai",
			origin:         "https://www.workbuddy.ai",
			acceptLanguage: "en-US",
			// Lower case, and not "WorkBuddy AI": the global 5.8.4 client sends
			// the slug form in the UA platform segment.
			platformLabel: "workbuddy-ai",
			clientVersion: "5.8.4",
			cliVersion:    "2.161.0",
		}
	}
	return endpoints{
		chatBase:    "https://www.workbuddy.cn",
		billingBase: "https://www.codebuddy.cn",
		// The chat/device domain is the one the CN client reports as its
		// origin; the billing domain stays on codebuddy.cn.
		origin:         "https://www.workbuddy.cn",
		acceptLanguage: "zh-CN",
		platformLabel:  "WorkBuddy",
		clientVersion:  "5.8.3",
		cliVersion:     "2.179.0",
	}
}

// Provider implements reverse.Provider and reverse.LoginProvider.
type Provider struct {
	mu            sync.Mutex
	catalogs      map[string]cachedCatalog
	efforts       map[string]map[string]modelEfforts
	degraded      map[int64]degradedPrompt
	clientFactory func(*reverse.Account) *http.Client
}

type cachedCatalog struct {
	models    []reverse.Model
	skipped   []reverse.SkippedModel
	fetchedAt time.Time
	lastFail  time.Time
}

// degradedPrompt records the prompt fingerprint that last tripped the content
// policy for an account, plus when it happened so the entry can be expired.
type degradedPrompt struct {
	key string
	at  time.Time
}

// modelEfforts is the reasoning-effort capability of one model.
type modelEfforts struct {
	supported []string
	fallback  string
}

var (
	_ reverse.Provider      = (*Provider)(nil)
	_ reverse.LoginProvider = (*Provider)(nil)
	_ reverse.CacheCleaner  = (*Provider)(nil)
)

// New builds the WorkBuddy provider.
func New() reverse.Provider {
	return &Provider{
		catalogs: map[string]cachedCatalog{},
		efforts:  map[string]map[string]modelEfforts{},
		degraded: map[int64]degradedPrompt{},
	}
}

// client returns the shared account-scoped HTTP client.
func (p *Provider) client(acc *reverse.Account) *http.Client {
	if p.clientFactory != nil {
		return p.clientFactory(acc)
	}
	if acc == nil {
		return reverse.HTTPClient("")
	}
	return reverse.HTTPClient(acc.ProxyURL)
}

// CleanupCaches drops catalogs, effort tables and degraded-prompt markers that
// are past their TTL. Live accounts refetch on demand; the point is to stop
// deleted or rotated accounts from accumulating entries forever.
func (p *Provider) CleanupCaches(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, entry := range p.catalogs {
		// Keep a stale catalog while its failure window is still active: that is
		// the last-good fallback ListModels serves after a failed probe. Evict
		// only once both timestamps are stale, which is also when a deleted
		// account has stopped refreshing either of them.
		staleFetch := entry.fetchedAt.IsZero() || now.Sub(entry.fetchedAt) >= modelCacheTTL
		staleFail := entry.lastFail.IsZero() || now.Sub(entry.lastFail) >= modelCacheFailWindow
		if staleFetch && staleFail {
			delete(p.catalogs, key)
			delete(p.efforts, key)
		}
	}
	// Effort tables are published just after their catalog; drop any that lost
	// their catalog entry so the two maps cannot drift apart.
	for key := range p.efforts {
		if _, ok := p.catalogs[key]; !ok {
			delete(p.efforts, key)
		}
	}
	for id, entry := range p.degraded {
		if entry.at.IsZero() || now.Sub(entry.at) >= degradedPromptTTL {
			delete(p.degraded, id)
		}
	}
}

// apiCall sends a request, reads a bounded body and unwraps the
// {code,msg,data} envelope. HTTP failures and non-zero business codes become
// *reverse.UpstreamError; endpoints that answer with a bare object pass
// through untouched.
func apiCall(client *http.Client, req *http.Request, limit int64, secrets ...string) (json.RawMessage, error) {
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("workbuddy %s request: %w", req.URL.Path, errDo)
	}
	defer func() { _ = resp.Body.Close() }()

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if errRead != nil {
		return nil, fmt.Errorf("workbuddy read %s response: %w", req.URL.Path, errRead)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("workbuddy %s response exceeds %d bytes", req.URL.Path, limit)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, upstreamError(resp.StatusCode, string(body), resp.Header, secrets...)
	}
	return unwrapData(body, secrets...)
}

// unwrapData returns the envelope payload, or the body itself when the
// response is not enveloped.
func unwrapData(body []byte, secrets ...string) (json.RawMessage, error) {
	var probe struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(body, &probe); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy: upstream returned a non-JSON response: %s", truncate(strings.TrimSpace(string(body)), 256))
	}
	if len(probe.Code) == 0 {
		// Direct object without an envelope.
		return body, nil
	}
	if code := strings.Trim(strings.TrimSpace(string(probe.Code)), `"`); code != "" && code != "0" && code != "null" {
		// A business error inside an HTTP 200 envelope is a request-level
		// failure. Synthesize 400 so Classify() can map bad params/content
		// instead of falling through to a 502 server error.
		return nil, upstreamError(http.StatusBadRequest, string(body), nil, secrets...)
	}
	if len(probe.Data) > 0 && strings.TrimSpace(string(probe.Data)) != "null" {
		return probe.Data, nil
	}
	return body, nil
}

// withTimeout bounds a short RPC.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, timeout)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func stringField(object map[string]any, key string) string {
	value, ok := object[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return fmt.Sprintf("%.0f", typed)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func nestedObject(object map[string]any, key string) map[string]any {
	nested, _ := object[key].(map[string]any)
	return nested
}

func nowUnixMilli() int64 { return time.Now().UnixMilli() }

func intFromAny(value any) int {
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
	}
	return 0
}
