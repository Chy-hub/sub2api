package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

const (
	bridgeTTL     = 30 * time.Minute
	modelCacheTTL = 10 * time.Minute
)

type bridgeEntry struct {
	bridge      *bridge
	fingerprint string
	createdAt   time.Time
}

type modelEntry struct {
	models      []reverse.Model
	resolution  map[string]string
	diagnostics *reverse.ModelCatalog
	fingerprint string
	fetchedAt   time.Time
}

// catalog returns the entry as a diagnostic catalogue.
func (e *modelEntry) catalog() *reverse.ModelCatalog {
	if e == nil {
		return nil
	}
	if e.diagnostics != nil {
		return e.diagnostics
	}
	return &reverse.ModelCatalog{Models: e.models, Resolution: e.resolution}
}

// Provider implements reverse.Provider and reverse.LoginProvider for Qoder.
type Provider struct {
	mu      sync.Mutex
	bridges map[int64]*bridgeEntry
	models  map[int64]*modelEntry
}

var (
	_ reverse.Provider      = (*Provider)(nil)
	_ reverse.LoginProvider = (*Provider)(nil)
	_ reverse.CacheCleaner  = (*Provider)(nil)
)

// New builds a Qoder provider with empty session and model caches.
func New() reverse.Provider {
	return &Provider{
		bridges: map[int64]*bridgeEntry{},
		models:  map[int64]*modelEntry{},
	}
}

func (p *Provider) bridgeFor(ctx context.Context, acc *reverse.Account) (*bridge, error) {
	if acc == nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "missing_account", "qoder account is nil")
	}
	fingerprint := credentialFingerprint(acc)
	p.mu.Lock()
	if entry := p.bridges[acc.ID]; entry != nil && entry.fingerprint == fingerprint && time.Since(entry.createdAt) < bridgeTTL {
		p.mu.Unlock()
		return entry.bridge, nil
	}
	p.mu.Unlock()

	template, err := templateBase()
	if err != nil {
		return nil, err
	}
	createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	created, err := newBridge(createCtx, acc, template)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.bridges[acc.ID] = &bridgeEntry{bridge: created, fingerprint: fingerprint, createdAt: time.Now()}
	p.mu.Unlock()
	return created, nil
}

func (p *Provider) invalidateBridge(accountID int64) {
	p.mu.Lock()
	delete(p.bridges, accountID)
	p.mu.Unlock()
}

// CleanupCaches drops bridge sessions and model catalogs past their TTL. Both
// are rebuilt on demand, so deleting an expired entry changes nothing for a
// live account; it only stops deleted accounts from lingering forever.
func (p *Provider) CleanupCaches(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, entry := range p.bridges {
		if entry == nil || now.Sub(entry.createdAt) >= bridgeTTL {
			delete(p.bridges, id)
		}
	}
	for id, entry := range p.models {
		if entry == nil || now.Sub(entry.fetchedAt) >= modelCacheTTL {
			delete(p.models, id)
		}
	}
}

// invalidateOnAuthError drops the cached cosy session when the upstream
// rejected it, so the next request rebuilds it.
func (p *Provider) invalidateOnAuthError(accountID int64, err error) {
	var upstreamErr *reverse.UpstreamError
	if err == nil || !errors.As(err, &upstreamErr) {
		return
	}
	if upstreamErr.StatusCode == http.StatusUnauthorized {
		p.invalidateBridge(accountID)
	}
}

// Forward adapts one Chat Completions request to the Qoder agent stream and
// returns a synthetic OpenAI response.
// credentialSecrets returns every non-empty credential that must never appear
// in an upstream error surfaced to clients or logs.
func credentialSecrets(acc *reverse.Account) []string {
	if acc == nil {
		return nil
	}
	seen := make(map[string]struct{}, 8)
	var out []string
	for _, key := range []string{
		"access_token", "refresh_token", "machine_token", "device_token",
		"personal_token", "security_oauth_token", "pat", "secret",
	} {
		value := strings.TrimSpace(acc.CredentialString(key))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (p *Provider) Forward(ctx context.Context, acc *reverse.Account, ccBody []byte, stream bool) (*http.Response, error) {
	if acc == nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "missing_account", "qoder account is nil")
	}
	request, err := decodeChatRequest(ccBody)
	if err != nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "invalid_request_error", err.Error())
	}
	br, err := p.bridgeFor(ctx, acc)
	if err != nil {
		p.invalidateOnAuthError(acc.ID, err)
		return nil, err
	}
	// A public model slug must be resolved from this account's regional catalog.
	// Raw upstream keys remain usable if discovery is temporarily unavailable.
	if p.cachedModels(acc) == nil && !strings.HasSuffix(strings.ToLower(request.model), "model") {
		// Catalog discovery is best-effort; never let it consume the chat
		// request's budget (the signed GET has no timeout of its own).
		listCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		_, _ = p.ListModels(listCtx, acc)
		cancel()
	}
	model := p.resolveModel(acc, request.model)
	envelope := buildEnvelope(br.template, request, model, br.session.Identity.UserType)

	upstream, err := p.openReadyChatStream(ctx, acc, br, envelope, model)
	if err != nil {
		p.invalidateOnAuthError(acc.ID, err)
		return nil, err
	}

	id := "chatcmpl-" + newRequestID()
	created := time.Now().Unix()
	secrets := credentialSecrets(acc)

	if stream {
		reader, writer := io.Pipe()
		go func() {
			defer func() { _ = upstream.Body.Close() }()
			streamErr := pumpStream(ctx, upstream.Body, writer, id, created, request.model, request.includeUsage, secrets...)
			p.invalidateOnAuthError(acc.ID, streamErr)
			_ = writer.CloseWithError(streamErr)
		}()
		header := http.Header{}
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("X-Accel-Buffering", "no")
		reverse.CopyCorrelationHeaders(header, upstream.Header)
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     header,
			Body:       reader,
		}, nil
	}

	state, err := consumeStream(upstream.Body, secrets...)
	_ = upstream.Body.Close()
	if err != nil {
		p.invalidateOnAuthError(acc.ID, err)
		return nil, err
	}
	body := buildCompletion(state, id, created, request.model)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	reverse.CopyCorrelationHeaders(header, upstream.Header)
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

func (p *Provider) openChatStream(ctx context.Context, acc *reverse.Account, br *bridge, envelope map[string]any, model string) (*http.Response, error) {
	plain, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode qoder request: %w", err)
	}
	body, err := encodeCustomBase64(plain)
	if err != nil {
		return nil, err
	}
	endpoint := endpointsFor(br.region).chat
	pathSig, err := pathSignature(endpoint)
	if err != nil {
		return nil, err
	}
	headers, err := buildHeaders(br.session, pathSig, body, "text/event-stream", map[string]string{
		"x-model-key":    model,
		"x-model-source": modelConfigSource(envelope),
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := reverse.HTTPClient(acc.ProxyURL).Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		raw, readErr := readBodyLimited(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read qoder stream response: %w", readErr)
		}
		return nil, upstreamError(response.StatusCode, response.Header, raw, accountAccessToken(acc), br.session.MachineToken)
	}
	return response, nil
}

// ListModels fetches the upstream model catalog with a cosy-signed GET and
// caches it for ten minutes.
func (p *Provider) ListModels(ctx context.Context, acc *reverse.Account) ([]reverse.Model, error) {
	catalog, err := p.ListModelCatalog(ctx, acc)
	if err != nil {
		return nil, err
	}
	return catalog.Models, nil
}

// ListModelCatalog returns the catalogue together with the parsing evidence.
//
// This is what makes "the vendor client lists a model the gateway does not"
// answerable: Categories/SelectedCategories show which upstream arrays existed,
// and Skipped shows every entry that was dropped with a reason code.
func (p *Provider) ListModelCatalog(ctx context.Context, acc *reverse.Account) (*reverse.ModelCatalog, error) {
	if acc == nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "missing_account", "qoder account is nil")
	}
	if cached := p.cachedModels(acc); cached != nil {
		return cached.catalog(), nil
	}
	br, err := p.bridgeFor(ctx, acc)
	if err != nil {
		p.invalidateOnAuthError(acc.ID, err)
		return nil, err
	}
	response, err := br.signedGet(ctx, reverse.HTTPClient(acc.ProxyURL), endpointsFor(br.region).modelList)
	if err != nil {
		p.invalidateOnAuthError(acc.ID, err)
		return nil, err
	}
	catalog, err := parseModels(response)
	if err != nil {
		return nil, err
	}
	if len(catalog.Models) == 0 {
		return nil, reverse.NewUpstreamError(http.StatusBadGateway, "empty_model_list", "qoder returned an empty model list")
	}
	p.mu.Lock()
	p.models[acc.ID] = &modelEntry{
		models:      catalog.Models,
		resolution:  catalog.Resolution,
		diagnostics: catalog,
		fingerprint: credentialFingerprint(acc),
		fetchedAt:   time.Now(),
	}
	p.mu.Unlock()
	return catalog, nil
}

func (p *Provider) cachedModels(acc *reverse.Account) *modelEntry {
	if acc == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry := p.models[acc.ID]
	if entry == nil || time.Since(entry.fetchedAt) >= modelCacheTTL || entry.fingerprint != credentialFingerprint(acc) {
		return nil
	}
	return entry
}

// Primary catalogue categories, in preference order.
//
// The upstream splits its catalogue across several top-level arrays. The first
// non-empty one used to be the only one read, which silently hid every model
// that only appeared in a later category — the exact failure that makes "the
// vendor client shows it, the gateway does not" unexplainable after the fact.
//
// These are the historical catalogue sources and are read in full, including
// entries carrying enable=false: that flag is reported per model rather than
// used as a filter. The upstream sets it on models that are still listed and
// still callable, so filtering by it here dropped models the vendor client
// happily shows.
var modelListCategories = []string{"assistant", "developer", "chat"}

// Secondary categories are product-line projections of the same catalogue.
//
// Only entries the upstream marks enable=true are taken from them. QoDuck
// (key=lmodel) exists only in "app" and is free (price_factor=0); without this
// pass it never reached /v1/models. In the other direction, the scenario-only
// keys these lists also carry (quest-auto, nap-auto, qwork-ultimate, …) are
// enable=false and would be noise in a catalogue of models a client can call.
var modelListSecondaryCategories = []string{
	"app", "qwork", "qwake", "inline", "quest", "experts", "nap",
	"byok_enterprise", "byok_teams",
}

// parseModels reads the upstream catalogue and records what it dropped.
func parseModels(response map[string]any) (*reverse.ModelCatalog, error) {
	catalog := &reverse.ModelCatalog{
		Categories: make(map[string]int, len(modelListCategories)+len(modelListSecondaryCategories)),
		Resolution: map[string]string{},
	}
	for _, category := range append(append([]string{}, modelListCategories...), modelListSecondaryCategories...) {
		items, _ := response[category].([]any)
		catalog.Categories[category] = len(items)
	}

	// Read every category, not just the first non-empty one, so a model that
	// only lives in a later list is still reachable. Entries already produced
	// by an earlier category win; the rest are reported as duplicates. Primary
	// categories come first so their projection wins on a shared key.
	selected := make([]string, 0, len(modelListCategories)+len(modelListSecondaryCategories))
	appendCategory := func(category string, onlyEnabled bool) {
		items, _ := response[category].([]any)
		if len(items) == 0 {
			return
		}
		models, resolution, skipped := extractModels(items, category, onlyEnabled)
		if len(models) == 0 && len(skipped) == 0 {
			return
		}
		selected = append(selected, category)
		for _, model := range models {
			if _, exists := catalog.Resolution[model.ID]; exists {
				catalog.Skipped = append(catalog.Skipped, reverse.SkippedModel{
					Key: model.ID, DisplayName: model.DisplayName,
					Reason: reverse.SkipReasonDuplicate, Category: category,
				})
				continue
			}
			catalog.Models = append(catalog.Models, model)
		}
		for alias, key := range resolution {
			if _, exists := catalog.Resolution[alias]; !exists {
				catalog.Resolution[alias] = key
			}
		}
		catalog.Skipped = append(catalog.Skipped, skipped...)
	}
	for _, category := range modelListCategories {
		appendCategory(category, false)
	}
	for _, category := range modelListSecondaryCategories {
		appendCategory(category, true)
	}
	catalog.SelectedCategories = selected
	return catalog, nil
}

// extractModels converts one upstream category into models plus the entries it
// dropped. A dropped entry is never silent: each one carries a reason code.
//
// onlyEnabled is set for the secondary categories: they are projections of the
// primary lists, so an entry the upstream marks disabled there is a
// scenario-only key rather than a callable model. Those are reported as
// other_category, not disabled — the model exists upstream, it simply belongs
// to a list this provider does not serve from.
func extractModels(items []any, category string, onlyEnabled bool) ([]reverse.Model, map[string]string, []reverse.SkippedModel) {
	models := make([]reverse.Model, 0, len(items))
	resolution := make(map[string]string, len(items)*2)
	skipped := make([]reverse.SkippedModel, 0)
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			skipped = append(skipped, reverse.SkippedModel{Reason: reverse.SkipReasonMissingKey, Category: category})
			continue
		}
		key := stringValue(entry["key"])
		name := stringValue(entry["display_name"])
		if onlyEnabled {
			if enabled, ok := entry["enable"].(bool); ok && !enabled {
				skipped = append(skipped, reverse.SkippedModel{Key: key, DisplayName: name, Reason: reverse.SkipReasonOtherCategory, Category: category})
				continue
			}
		}
		if key == "" {
			skipped = append(skipped, reverse.SkippedModel{Key: key, DisplayName: name, Reason: reverse.SkipReasonMissingKey, Category: category})
			continue
		}
		if name == "" {
			name = key
		}
		id := canonicalModelID(name, key)
		if _, duplicate := seen[id]; duplicate {
			skipped = append(skipped, reverse.SkippedModel{Key: key, DisplayName: name, Reason: reverse.SkipReasonDuplicate, Category: category})
			continue
		}
		seen[id] = struct{}{}
		contextLength := int64(intValue(entry["context_window"]))
		if configs, ok := entry["context_config"].(map[string]any); ok {
			keys := make([]string, 0, len(configs))
			for key := range configs {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			var fallback int64
			for _, key := range keys {
				config, _ := configs[key].(map[string]any)
				count := int64(intValue(config["token_count"]))
				if fallback == 0 {
					fallback = count
				}
				if config["is_default"] == true && count > 0 {
					contextLength = count
					break
				}
			}
			if contextLength <= 0 {
				contextLength = fallback
			}
		}
		if contextLength <= 0 {
			contextLength = int64(intValue(entry["max_input_tokens"]))
		}
		reasoning := entry["is_reasoning"] == true
		outputTokens := int64(intValue(entry["max_output_tokens"]))
		if outputTokens <= 0 {
			outputTokens = 16384
			if reasoning {
				outputTokens = 32768
			}
		}
		models = append(models, reverse.Model{ID: id, DisplayName: name, Description: stringValue(entry["description"]), ContextLength: contextLength, MaxOutputTokens: outputTokens, Credits: stringValue(entry["price_factor"]), SupportsTools: true, SupportsReasoning: reasoning, CanDisableThinking: !reasoning})
		for _, alias := range []string{strings.ToLower(key), strings.ToLower(name), canonicalModelID(name, key)} {
			if alias == "" {
				continue
			}
			if _, exists := resolution[alias]; !exists {
				resolution[alias] = key
			}
		}
	}
	return models, resolution, skipped
}

// canonicalModelID slugifies a display name the way the plugin's model index
// does ("DeepSeek-V4-Pro" -> "deepseek-v4-pro").
func canonicalModelID(displayName, key string) string {
	slug := strings.ToLower(strings.TrimSpace(displayName))
	slug = strings.Join(strings.Fields(slug), "-")
	if slug == "" {
		slug = strings.ToLower(strings.TrimSpace(key))
	}
	return slug
}

// defaultModelMapping mirrors the plugin's family fallback for clients that
// send a foreign model name (claude-sonnet-4-6 -> gmodel).
var defaultModelMapping = map[string]string{
	"opus":   "qmodel_38max",
	"sonnet": "gmodel",
	"haiku":  "qfmodel",
	"gpt":    "dmodel",
	"gemini": "gmodel",
}

func (p *Provider) resolveModel(acc *reverse.Account, model string) string {
	key, _ := p.resolveModelWithSource(acc, model)
	return key
}

// resolveModelWithSource resolves a public model name to the upstream key and
// reports how the match was made.
//
// The source matters because the upstream does not reject unknown model names:
// it silently answers with a default model. A request for a model that does not
// exist therefore looks identical to a successful one, which makes the account
// "test" endpoint unable to distinguish a real model from a typo. Callers that
// need that distinction (the test endpoint, diagnostics) read the source
// instead of guessing.
func (p *Provider) resolveModelWithSource(acc *reverse.Account, model string) (key string, source string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "auto", reverse.ModelSourceDefault
	}
	if cache := p.cachedModels(acc); cache != nil {
		if key, ok := cache.resolution[strings.ToLower(model)]; ok {
			return key, reverse.ModelSourceCatalog
		}
	}
	if mapped := lookupDefaultModel(model); mapped != "" {
		return mapped, reverse.ModelSourceFamily
	}
	return strings.ToLower(model), reverse.ModelSourcePassthrough
}

func lookupDefaultModel(model string) string {
	if mapped, ok := defaultModelMapping[model]; ok {
		return mapped
	}
	keys := make([]string, 0, len(defaultModelMapping))
	for key := range defaultModelMapping {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	lower := strings.ToLower(model)
	for _, key := range keys {
		// The reverse direction needs a minimum length: a one-letter model
		// name would otherwise match "haiku" and silently pick a mapping.
		if strings.Contains(lower, key) || (len(lower) >= 4 && strings.Contains(key, lower)) {
			return defaultModelMapping[key]
		}
	}
	return ""
}

// Refresh validates and rotates the account credentials.
func (p *Provider) Refresh(ctx context.Context, acc *reverse.Account) (map[string]any, error) {
	if acc == nil {
		return nil, reverse.NewUpstreamError(http.StatusBadRequest, "missing_account", "qoder account is nil")
	}
	updated, err := refreshCredentials(ctx, acc)
	if err != nil {
		return nil, err
	}
	p.invalidateBridge(acc.ID)
	return updated, nil
}

// Test sends a short non-stream prompt through the normal Forward path.
//
// The upstream answers unknown model names with a default model instead of an
// error, so a typo used to look like a successful test. Resolution is checked
// first: a name that matches neither the account's catalogue nor a family alias
// is reported as such rather than silently passing.
func (p *Provider) Test(ctx context.Context, acc *reverse.Account, model, prompt string) (*reverse.TestResult, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = "hi"
	}
	if strings.TrimSpace(model) == "" {
		model = "auto"
	}
	// Reject names the account cannot serve before spending an upstream call:
	// a passthrough name is answered with a default model upstream, which would
	// otherwise be reported as a successful test.
	if _, errCatalog := p.ListModelCatalog(ctx, acc); errCatalog == nil {
		if key, source := p.resolveModelWithSource(acc, model); source == reverse.ModelSourcePassthrough {
			return nil, reverse.NewUpstreamError(http.StatusBadRequest, "model_not_in_catalog",
				fmt.Sprintf("model %q is not in this account's catalogue (upstream would answer with a default model instead)", model))
		} else if source == reverse.ModelSourceCatalog {
			model = key
		}
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": prompt},
		},
		"max_tokens": 512,
		"stream":     false,
	})
	response, err := p.Forward(ctx, acc, body, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := readBodyLimited(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read qoder test response: %w", err)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode qoder test response: %w", err)
	}
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	return &reverse.TestResult{Content: content}, nil
}
