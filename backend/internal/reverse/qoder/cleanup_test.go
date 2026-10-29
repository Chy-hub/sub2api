package qoder

import (
	"testing"
	"time"
)

func TestProviderCleanupCachesDropsExpiredEntries(t *testing.T) {
	provider := New().(*Provider)
	now := time.Now()
	provider.bridges[1] = &bridgeEntry{createdAt: now.Add(-2 * bridgeTTL)}
	provider.bridges[2] = &bridgeEntry{createdAt: now}
	provider.models[1] = &modelEntry{fetchedAt: now.Add(-2 * modelCacheTTL)}
	provider.models[2] = &modelEntry{fetchedAt: now}

	provider.CleanupCaches(now)

	if _, ok := provider.bridges[1]; ok {
		t.Fatal("expired bridge should be evicted")
	}
	if _, ok := provider.bridges[2]; !ok {
		t.Fatal("fresh bridge should be kept")
	}
	if _, ok := provider.models[1]; ok {
		t.Fatal("expired model cache should be evicted")
	}
	if _, ok := provider.models[2]; !ok {
		t.Fatal("fresh model cache should be kept")
	}
}
