package reverse

import (
	"strings"
	"time"
)

// CacheCleaner is implemented by providers that keep in-memory caches keyed by
// account. The gateway calls it periodically so deleted or rotated accounts do
// not accumulate entries for the lifetime of the process.
type CacheCleaner interface {
	CleanupCaches(now time.Time)
}

// Registry maps provider names (qoder / workbuddy) to implementations.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. Empty names and nil providers are ignored.
func (r *Registry) Register(name string, provider Provider) {
	if r == nil || provider == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.providers[name] = provider
}

// Get returns a registered provider.
func (r *Registry) Get(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[strings.TrimSpace(name)]
	return provider, ok
}

// Login returns the provider's login implementation when available.
func (r *Registry) Login(name string) (LoginProvider, bool) {
	provider, ok := r.Get(name)
	if !ok {
		return nil, false
	}
	login, ok := provider.(LoginProvider)
	return login, ok
}

// CleanupCaches asks every registered provider to drop expired cache entries.
func (r *Registry) CleanupCaches(now time.Time) {
	if r == nil {
		return
	}
	for _, provider := range r.providers {
		if cleaner, ok := provider.(CacheCleaner); ok {
			cleaner.CleanupCaches(now)
		}
	}
}
