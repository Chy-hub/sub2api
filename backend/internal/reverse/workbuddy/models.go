package workbuddy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// modelCacheFailWindow throttles retries after a failed probe.
const modelCacheFailWindow = 5 * time.Minute

// dynModelEntry is one model entry from /v3/config or an enterprise catalog.
type dynModelEntry struct {
	Credits         string   `json:"credits"`
	MaxAllowedSize  int64    `json:"maxAllowedSize"`
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	ModelID         string   `json:"modelId"`
	Model           string   `json:"model"`
	Description     string   `json:"descriptionZh"`
	Tags            []string `json:"tags"`
	IsDefault       bool     `json:"isDefault"`
	MaxInputTokens  int64    `json:"maxInputTokens"`
	MaxOutputTokens int64    `json:"maxOutputTokens"`
	Disabled        bool     `json:"disabled"`
	SupportsImages  bool     `json:"supportsImages"`
	SupportsReason  bool     `json:"supportsReasoning"`
	SupportsTool    bool     `json:"supportsToolCall"`
	OnlyReasoning   bool     `json:"onlyReasoning"`
	Reasoning       struct {
		Effort           string   `json:"effort"`
		DefaultEffort    string   `json:"defaultEffort"`
		SupportedEfforts []string `json:"supportedEfforts"`
	} `json:"reasoning"`
}

// resolvedID returns the model key with the upstream's fallback chain.
func (e dynModelEntry) resolvedID() string {
	for _, candidate := range []string{e.ID, e.ModelID, e.Model} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(e.Name)
}

func (e dynModelEntry) displayName() string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return name
	}
	return e.resolvedID()
}

func (e dynModelEntry) efforts() modelEfforts {
	return modelEfforts{
		supported: normalizeEfforts(e.Reasoning.SupportedEfforts),
		fallback:  firstNonEmpty(e.Reasoning.DefaultEffort, e.Reasoning.Effort),
	}
}

// nonChatModel rejects entries that cannot serve chat (selecting them yields
// 11102): embedding/completion/code-only ids, tiny outputs, image generation.
func nonChatModel(entry dynModelEntry) bool {
	id := strings.ToLower(entry.resolvedID())
	for _, prefix := range []string{"nes-", "completion-", "codewise-"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	if entry.MaxOutputTokens > 0 && entry.MaxOutputTokens <= 256 {
		return true
	}
	for _, tag := range entry.Tags {
		if strings.EqualFold(strings.TrimSpace(tag), "text-to-image") {
			return true
		}
	}
	return false
}

func normalizeEfforts(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		effort := strings.ToLower(strings.TrimSpace(item))
		if effort == "" || seen[effort] {
			continue
		}
		seen[effort] = true
		out = append(out, effort)
	}
	return out
}

// v3ConfigResponse is the /v3/config payload (envelope already unwrapped).
type v3ConfigResponse struct {
	Models                []dynModelEntry `json:"models"`
	ProductFeaturesConfig struct {
		ModelTrialBanner struct {
			Banners []struct {
				ModelID       string `json:"modelId"`
				TargetModelID string `json:"targetModelId"`
			} `json:"banners"`
		} `json:"ModelTrialBanner"`
	} `json:"productFeaturesConfig"`
}

// enterpriseModelsResponse is the enterprise catalog payload.
type enterpriseModelsResponse struct {
	Agents []struct {
		Name   string   `json:"name"`
		Models []string `json:"models"`
	} `json:"agents"`
	Models []dynModelEntry `json:"models"`
}

// ListModels returns the account's model catalog, cached for ~10 minutes.
// A failed probe falls back to the last successful catalog, then to the
// embedded seed.
func (p *Provider) ListModels(ctx context.Context, acc *reverse.Account) ([]reverse.Model, error) {
	if acc == nil {
		return nil, errors.New("workbuddy: account is required")
	}
	cred := newCredential(acc)
	key := catalogKey(acc)

	p.mu.Lock()
	// Credentials rotate regularly. Retain only the current catalog and effort
	// table for an account, instead of accumulating obsolete token fingerprints.
	for oldKey := range p.catalogs {
		if oldKey != key && strings.HasPrefix(oldKey, fmt.Sprintf("%d|", acc.ID)) {
			delete(p.catalogs, oldKey)
			delete(p.efforts, oldKey)
		}
	}
	entry, hasEntry := p.catalogs[key]
	p.mu.Unlock()
	if hasEntry && len(entry.models) > 0 {
		if time.Since(entry.fetchedAt) < modelCacheTTL || time.Since(entry.lastFail) < modelCacheFailWindow {
			return entry.models, nil
		}
	}

	models, efforts, errFetch := p.fetchCatalog(ctx, acc, cred)
	if errFetch != nil {
		if hasEntry && len(entry.models) > 0 {
			p.mu.Lock()
			entry.lastFail = time.Now()
			p.catalogs[key] = entry
			p.mu.Unlock()
			return entry.models, nil
		}
		if seed := seedModels(cred.region); len(seed) > 0 {
			p.mu.Lock()
			p.catalogs[key] = cachedCatalog{models: seed, lastFail: time.Now()}
			p.mu.Unlock()
			p.publishEfforts(key, seedEfforts(cred.region))
			return seed, nil
		}
		return nil, errFetch
	}

	p.mu.Lock()
	p.catalogs[key] = cachedCatalog{models: models, fetchedAt: time.Now()}
	p.mu.Unlock()
	p.publishEfforts(key, efforts)
	return models, nil
}

func catalogKey(acc *reverse.Account) string {
	raw, _ := json.Marshal(acc.Credentials)
	sum := sha256.Sum256(append(raw, []byte(acc.ProxyURL)...))
	return fmt.Sprintf("%d|%s|%x", acc.ID, acc.Region, sum[:12])
}

// publishEfforts merges model effort capabilities into the region table used
// by the request pipeline.
func (p *Provider) publishEfforts(key string, efforts map[string]modelEfforts) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.efforts[key] = map[string]modelEfforts{}
	for model, entry := range efforts {
		if len(entry.supported) > 0 {
			p.efforts[key][model] = entry
		}
	}
}

// fetchCatalog probes /v3/config and the enterprise catalog, merging unique
// models with /v3/config winning field precedence.
func (p *Provider) fetchCatalog(ctx context.Context, acc *reverse.Account, cred *credential) ([]reverse.Model, map[string]modelEfforts, error) {
	callCtx, cancel := withTimeout(ctx, shortCallTimeout)
	defer cancel()
	client := p.client(acc)

	type probeResult struct {
		entries []dynModelEntry
		err     error
	}
	var probes [3]probeResult
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		probes[0].entries, probes[0].err = p.probeV3Config(callCtx, client, cred, codeBuddyIDEUA)
	}()
	go func() {
		defer wg.Done()
		probes[1].entries, probes[1].err = p.probeV3Config(callCtx, client, cred, loginClientUA)
	}()
	go func() {
		defer wg.Done()
		probes[2].entries, probes[2].err = p.probeEnterpriseModels(callCtx, client, cred)
	}()
	wg.Wait()
	primaryEntries := append(probes[0].entries, probes[1].entries...)
	errPrimary := probes[0].err
	enterpriseEntries, errEnterprise := probes[2].entries, probes[2].err

	merged := make([]reverse.Model, 0, len(primaryEntries)+len(enterpriseEntries))
	efforts := map[string]modelEfforts{}
	seen := map[string]bool{}

	appendEntries := func(entries []dynModelEntry) {
		for _, entry := range entries {
			id := entry.resolvedID()
			if id == "" || entry.Disabled || nonChatModel(entry) {
				continue
			}
			if capability := entry.efforts(); len(capability.supported) > 0 {
				if _, exists := efforts[id]; !exists {
					efforts[id] = capability
				}
			}
			if seen[id] {
				for i := range merged {
					if merged[i].ID != id {
						continue
					}
					if merged[i].Description == "" {
						merged[i].Description = entry.Description
					}
					if merged[i].Credits == "" {
						merged[i].Credits = entry.Credits
					}
					if merged[i].ContextLength == 0 {
						merged[i].ContextLength = entry.MaxInputTokens
					}
					if merged[i].MaxOutputTokens == 0 {
						merged[i].MaxOutputTokens = entry.MaxOutputTokens
					}
					break
				}
				continue
			}
			seen[id] = true
			capability := entry.efforts()
			contextLength := entry.MaxInputTokens
			if contextLength == 0 {
				contextLength = entry.MaxAllowedSize
			}
			merged = append(merged, reverse.Model{ID: id, DisplayName: entry.displayName(), Description: entry.Description, ContextLength: contextLength, MaxOutputTokens: entry.MaxOutputTokens, Efforts: capability.supported, DefaultEffort: capability.fallback, Credits: entry.Credits, SupportsImages: entry.SupportsImages, SupportsTools: entry.SupportsTool, SupportsReasoning: entry.SupportsReason || entry.OnlyReasoning, CanDisableThinking: !entry.OnlyReasoning})
		}
	}
	appendEntries(primaryEntries)
	appendEntries(enterpriseEntries)

	if len(merged) == 0 {
		switch {
		case errPrimary != nil && errEnterprise != nil:
			return nil, nil, fmt.Errorf("workbuddy model probe: %v; %v", errPrimary, errEnterprise)
		case errPrimary != nil:
			return nil, nil, fmt.Errorf("workbuddy model probe: %w", errPrimary)
		case errEnterprise != nil:
			return nil, nil, fmt.Errorf("workbuddy model probe: %w", errEnterprise)
		default:
			return nil, nil, errors.New("workbuddy: model probe returned no models")
		}
	}

	// Fill missing reasoning capabilities from the static per-region seed.
	for id, capability := range seedEfforts(cred.region) {
		if _, exists := efforts[id]; !exists {
			efforts[id] = capability
		}
	}
	for i := range merged {
		model := &merged[i]
		if seed, ok := loadSeed().Context[model.ID]; ok {
			if model.ContextLength <= 0 {
				model.ContextLength = seed.Context
			}
			if model.MaxOutputTokens <= 0 {
				model.MaxOutputTokens = seed.MaxOutput
			}
		}
		if capability, ok := efforts[model.ID]; ok {
			model.Efforts = capability.supported
			model.DefaultEffort = capability.fallback
			model.SupportsReasoning = model.SupportsReasoning || hasReasoningEffort(capability.supported)
		}
	}
	return merged, efforts, nil
}

// probeV3Config fetches the IDE model catalog.
func (p *Provider) probeV3Config(ctx context.Context, client *http.Client, cred *credential, agents ...string) ([]dynModelEntry, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpointsFor(cred.region).chatBase+v3ConfigPath, nil)
	if errReq != nil {
		return nil, fmt.Errorf("workbuddy build v3 config request: %w", errReq)
	}
	applyConfigHeaders(req, cred, codeBuddyIDEUA)
	if len(agents) > 0 {
		applyConfigHeaders(req, cred, agents[0])
	}

	raw, errCall := apiCall(client, req, configBodyLimit)
	if errCall != nil {
		return nil, errCall
	}
	var payload v3ConfigResponse
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy decode v3 config: %w", errUnmarshal)
	}

	entries := make([]dynModelEntry, 0, len(payload.Models))
	entries = append(entries, payload.Models...)
	// Trial models are callable but only listed in the banner config.
	for _, banner := range payload.ProductFeaturesConfig.ModelTrialBanner.Banners {
		trialID := strings.TrimSpace(banner.ModelID)
		if trialID == "" || containsEntryID(entries, trialID) {
			continue
		}
		trial := dynModelEntry{ID: trialID, Name: trialID}
		if base, okBase := findEntry(entries, strings.TrimSpace(banner.TargetModelID)); okBase {
			trial = base
			trial.ID = trialID
			trial.Name = trialID
			// The banner model itself is callable even if the target metadata
			// was flagged; keep it out of the disabled filter.
			trial.Disabled = false
		}
		entries = append(entries, trial)
	}
	return entries, nil
}

// probeEnterpriseModels fetches the enterprise catalog. CN uses the console
// path; Global prefers /v2 and only falls back on 404.
func (p *Provider) probeEnterpriseModels(ctx context.Context, client *http.Client, cred *credential) ([]dynModelEntry, error) {
	paths := []string{enterpriseModelsPathCN}
	if cred.region.isGlobal() {
		paths = []string{enterpriseModelsPathGlobal, enterpriseModelsPathCN}
	}

	var lastErr error
	for _, path := range paths {
		req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, endpointsFor(cred.region).chatBase+path, nil)
		if errReq != nil {
			lastErr = errReq
			continue
		}
		applyConfigHeaders(req, cred, codeBuddyIDEUA)

		raw, errCall := apiCall(client, req, configBodyLimit)
		if errCall != nil {
			lastErr = errCall
			// Only a 404 justifies trying the next path.
			var upstream *reverse.UpstreamError
			if errors.As(errCall, &upstream) && upstream.StatusCode == http.StatusNotFound {
				continue
			}
			return nil, errCall
		}

		var payload enterpriseModelsResponse
		if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
			lastErr = fmt.Errorf("workbuddy decode enterprise models: %w", errUnmarshal)
			continue
		}
		return parseEnterpriseModels(payload), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("workbuddy: enterprise models endpoint is unavailable")
}

// parseEnterpriseModels orders entries by the CLI agent whitelist (the
// upstream encodes its recommendation order there), then fills fields from the
// models table.
func parseEnterpriseModels(payload enterpriseModelsResponse) []dynModelEntry {
	byID := make(map[string]dynModelEntry, len(payload.Models))
	for _, entry := range payload.Models {
		if id := entry.resolvedID(); id != "" {
			byID[id] = entry
		}
	}

	order := make([]string, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	for _, agent := range payload.Agents {
		if !strings.EqualFold(strings.TrimSpace(agent.Name), "cli") {
			continue
		}
		for _, id := range agent.Models {
			trimmed := strings.TrimSpace(id)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			order = append(order, trimmed)
		}
	}
	if len(order) == 0 {
		for _, entry := range payload.Models {
			if id := entry.resolvedID(); id != "" && !seen[id] {
				seen[id] = true
				order = append(order, id)
			}
		}
	}

	out := make([]dynModelEntry, 0, len(order))
	for _, id := range order {
		entry, okEntry := byID[id]
		if !okEntry {
			out = append(out, dynModelEntry{ID: id, Name: id})
			continue
		}
		out = append(out, entry)
	}
	return out
}

func containsEntryID(entries []dynModelEntry, id string) bool {
	_, okFound := findEntry(entries, id)
	return okFound
}

func findEntry(entries []dynModelEntry, id string) (dynModelEntry, bool) {
	for _, entry := range entries {
		if entry.resolvedID() == id {
			return entry, true
		}
	}
	return dynModelEntry{}, false
}

// catalogSeed is the embedded per-region capability table.
type catalogSeed struct {
	Context map[string]struct {
		Context   int64 `json:"context"`
		MaxOutput int64 `json:"max_output"`
	} `json:"context"`
	EffortsCN     map[string]effortSeed `json:"efforts_cn"`
	EffortsGlobal map[string]effortSeed `json:"efforts_global"`
}

type effortSeed struct {
	Efforts []string `json:"efforts"`
	Default string   `json:"default"`
}

var seedOnceCache = struct {
	once sync.Once
	data catalogSeed
}{}

func loadSeed() catalogSeed {
	seedOnceCache.once.Do(func() {
		if errUnmarshal := json.Unmarshal(catalogSeedRaw, &seedOnceCache.data); errUnmarshal != nil {
			seedOnceCache.data = catalogSeed{}
		}
	})
	return seedOnceCache.data
}

func seedEffortTable(reg region) map[string]effortSeed {
	seed := loadSeed()
	if reg.isGlobal() {
		return seed.EffortsGlobal
	}
	return seed.EffortsCN
}

func seedEfforts(reg region) map[string]modelEfforts {
	table := seedEffortTable(reg)
	out := make(map[string]modelEfforts, len(table))
	for model := range table {
		entry, _ := seedEffortFor(reg, model)
		out[model] = entry
	}
	return out
}

// seedEffortFor looks up one model in the embedded per-region table.
func seedEffortFor(reg region, model string) (modelEfforts, bool) {
	entry, okEntry := seedEffortTable(reg)[strings.TrimSpace(model)]
	if !okEntry {
		return modelEfforts{}, false
	}
	return modelEfforts{
		supported: normalizeEfforts(entry.Efforts),
		fallback:  strings.TrimSpace(entry.Default),
	}, true
}

// seedModels builds a stable fallback catalog from the seed effort tables.
func seedModels(reg region) []reverse.Model {
	table := seedEffortTable(reg)
	models := make([]reverse.Model, 0, len(table))
	for model, entry := range table {
		limits := loadSeed().Context[model]
		models = append(models, reverse.Model{ID: model, DisplayName: model, ContextLength: limits.Context, MaxOutputTokens: limits.MaxOutput, Efforts: normalizeEfforts(entry.Efforts), DefaultEffort: entry.Default, SupportsReasoning: hasReasoningEffort(entry.Efforts), CanDisableThinking: containsString(entry.Efforts, "none")})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

func hasReasoningEffort(efforts []string) bool {
	for _, effort := range efforts {
		if effort != "" && effort != "none" {
			return true
		}
	}
	return false
}
