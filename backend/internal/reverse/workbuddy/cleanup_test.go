package workbuddy

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

func TestProviderCleanupCachesDropsExpiredEntries(t *testing.T) {
	provider := New().(*Provider)
	now := time.Now()
	stale, fresh := "1|cn|aa", "2|cn|bb"
	provider.catalogs[stale] = cachedCatalog{models: []reverse.Model{{ID: "m"}}, fetchedAt: now.Add(-2 * modelCacheTTL)}
	provider.efforts[stale] = map[string]modelEfforts{"m": {supported: []string{"high"}}}
	provider.catalogs[fresh] = cachedCatalog{models: []reverse.Model{{ID: "m"}}, fetchedAt: now}
	provider.efforts[fresh] = map[string]modelEfforts{"m": {supported: []string{"high"}}}
	// Failure/seed fallbacks only carry lastFail.
	provider.catalogs["seed"] = cachedCatalog{models: []reverse.Model{{ID: "s"}}, lastFail: now.Add(-2 * modelCacheFailWindow)}
	// A stale catalog whose probe failed recently is still the last-good fallback.
	provider.catalogs["fallback"] = cachedCatalog{models: []reverse.Model{{ID: "f"}}, fetchedAt: now.Add(-2 * modelCacheTTL), lastFail: now}
	provider.efforts["fallback"] = map[string]modelEfforts{"m": {supported: []string{"high"}}}
	provider.efforts["orphan"] = map[string]modelEfforts{"m": {supported: []string{"high"}}}
	provider.degraded[1] = degradedPrompt{key: "x", at: now.Add(-2 * degradedPromptTTL)}
	provider.degraded[2] = degradedPrompt{key: "y", at: now}

	provider.CleanupCaches(now)

	for _, key := range []string{stale, "seed", "orphan"} {
		if _, ok := provider.catalogs[key]; ok {
			t.Fatalf("catalog %q should be evicted", key)
		}
		if _, ok := provider.efforts[key]; ok {
			t.Fatalf("efforts %q should be evicted", key)
		}
	}
	if _, ok := provider.catalogs[fresh]; !ok {
		t.Fatal("fresh catalog should be kept")
	}
	if _, ok := provider.efforts[fresh]; !ok {
		t.Fatal("fresh efforts should be kept")
	}
	if _, ok := provider.catalogs["fallback"]; !ok {
		t.Fatal("catalog with an active failure window should be kept")
	}
	if _, ok := provider.efforts["fallback"]; !ok {
		t.Fatal("efforts for an active failure-window catalog should be kept")
	}
	if _, ok := provider.degraded[1]; ok {
		t.Fatal("expired degraded marker should be evicted")
	}
	if _, ok := provider.degraded[2]; !ok {
		t.Fatal("fresh degraded marker should be kept")
	}
}
