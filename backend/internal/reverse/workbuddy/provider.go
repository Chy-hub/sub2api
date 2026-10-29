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

// Outbound identity. Versions mirror the genuine 5.8.3 client; the platform
// segment must be "WorkBuddy AI" on the global realm or the upstream answers
// 403 code=11140 "request illegal".
const (
	clientVersion = "5.8.3"
	cliVersion    = "2.161.0"
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
type endpoints struct {
	chatBase       string
	billingBase    string
	origin         string
	acceptLanguage string
	platformLabel  string
}

func endpointsFor(r region) endpoints {
	if r.isGlobal() {
		return endpoints{
			chatBase:       "https://www.workbuddy.ai",
			billingBase:    "https://www.workbuddy.ai",
			origin:         "https://www.workbuddy.ai",
			acceptLanguage: "en-US",
			platformLabel:  "WorkBuddy AI",
		}
	}
	return endpoints{
		chatBase:       "https://www.workbuddy.cn",
		billingBase:    "https://www.codebuddy.cn",
		origin:         "https://www.codebuddy.cn",
		acceptLanguage: "zh-CN",
		platformLabel:  "WorkBuddy",
	}
}

// Provider implements reverse.Provider and reverse.LoginProvider.
type Provider struct {
	mu            sync.Mutex
	catalogs      map[string]cachedCatalog
	efforts       map[string]map[string]modelEfforts
	degraded      map[int64]string
	clientFactory func(*reverse.Account) *http.Client
}

type cachedCatalog struct {
	models    []reverse.Model
	fetchedAt time.Time
	lastFail  time.Time
}

// modelEfforts is the reasoning-effort capability of one model.
type modelEfforts struct {
	supported []string
	fallback  string
}

var (
	_ reverse.Provider      = (*Provider)(nil)
	_ reverse.LoginProvider = (*Provider)(nil)
)

// New builds the WorkBuddy provider.
func New() reverse.Provider {
	return &Provider{
		catalogs: map[string]cachedCatalog{},
		efforts:  map[string]map[string]modelEfforts{},
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

// apiCall sends a request, reads a bounded body and unwraps the
// {code,msg,data} envelope. HTTP failures and non-zero business codes become
// *reverse.UpstreamError; endpoints that answer with a bare object pass
// through untouched.
func apiCall(client *http.Client, req *http.Request, limit int64) (json.RawMessage, error) {
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
		return nil, upstreamError(resp.StatusCode, string(body), resp.Header)
	}
	return unwrapData(body)
}

// unwrapData returns the envelope payload, or the body itself when the
// response is not enveloped.
func unwrapData(body []byte) (json.RawMessage, error) {
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
		message := strings.TrimSpace(probe.Msg)
		if message == "" {
			message = truncate(strings.TrimSpace(string(body)), 512)
		}
		return nil, upstreamError(0, string(body), nil)
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
