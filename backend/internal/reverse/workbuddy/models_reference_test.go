package workbuddy

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/stretchr/testify/require"
)

// Regression cases ported from CLIProxyAPI-freetier2api-plugin (MIT).

func TestNonChatModelFiltersVideoAndAudioFamilies(t *testing.T) {
	// The upstream catalogue lists generation models like ordinary chat models,
	// but calling them fails with 400 "Backend [maas] is not supported". Keep
	// them out of the listing by family name and by generation tags.
	blocked := []dynModelEntry{
		{Model: "kling-video-v3"},
		{Model: "seedance-2.5"},
		{Model: "sora-2"},
		{Model: "veo-3"},
		{Model: "hailuo-video"},
		{Model: "jimeng-video"},
		{Model: "vidu-q1"},
		{Model: "pixverse-v4"},
		{Model: "runway-gen4"},
		{Model: "luma-ray3"},
		{Model: "cogvideo-x"},
		{Model: "wan2.2"},
		{Model: "hunyuanvideo"},
		{Model: "minimax-video-01"},
		{Model: "some-chat-model", Tags: []string{"text-to-video"}},
		{Model: "some-chat-model", Tags: []string{"image-to-video"}},
		{Model: "some-chat-model", Tags: []string{"video-generation"}},
		{Model: "some-chat-model", Tags: []string{"text-to-speech"}},
		{Model: "some-chat-model", Tags: []string{"audio-generation"}},
		{Model: "some-chat-model", Tags: []string{"TEXT-TO-IMAGE"}},
	}
	for _, entry := range blocked {
		if !nonChatModel(entry) {
			t.Errorf("nonChatModel(%q, tags=%v) = false, want filtered", entry.resolvedID(), entry.Tags)
		}
	}

	// A genuine chat model must survive the wider filter.
	if nonChatModel(dynModelEntry{Model: "deepseek-v4-flash"}) {
		t.Fatal("chat model was filtered as non-chat")
	}
}

func TestBlockedModelsAreFilteredPerRealm(t *testing.T) {
	// 11102 is a routing-table fact, so one observation hides the model from
	// every account in that realm — but the same name in the other realm is an
	// independent entry and must stay visible.
	p := &Provider{blockedModels: map[string]time.Time{}}
	p.markModelBlocked(regionGlobal, "o4-mini")

	catalog := &reverse.ModelCatalog{Models: []reverse.Model{
		{ID: "o4-mini", DisplayName: "o4-mini"},
		{ID: "deepseek-v4-flash", DisplayName: "DeepSeek V4 Flash"},
	}}
	filtered := p.filterBlockedModels(regionGlobal, catalog)
	require.Len(t, filtered.Models, 1)
	require.Equal(t, "deepseek-v4-flash", filtered.Models[0].ID)
	require.Len(t, filtered.Skipped, 1)
	require.Equal(t, "o4-mini", filtered.Skipped[0].Key)
	require.Equal(t, reverse.SkipReasonModelBlocked, filtered.Skipped[0].Reason)

	otherRealm := p.filterBlockedModels(regionCN, catalog)
	require.Len(t, otherRealm.Models, 2, "isolation must not cross realms")
	require.Empty(t, otherRealm.Skipped)
}

func TestBlockedModelIsolationExpires(t *testing.T) {
	p := &Provider{blockedModels: map[string]time.Time{}}
	p.markModelBlocked(regionGlobal, "o4-mini")
	p.mu.Lock()
	p.blockedModels[blockedModelKey(regionGlobal, "o4-mini")] = time.Now().Add(-blockedModelTTL - time.Minute)
	p.mu.Unlock()

	catalog := &reverse.ModelCatalog{Models: []reverse.Model{{ID: "o4-mini"}}}
	filtered := p.filterBlockedModels(regionGlobal, catalog)
	require.Len(t, filtered.Models, 1, "expired isolation must let the model return")
	require.Empty(t, filtered.Skipped)
}

func TestBlockedModelsNeverEmptyTheCatalog(t *testing.T) {
	// An empty listing would make the platform unusable for every account in
	// the realm; keeping one possibly-dead entry is the lesser evil.
	p := &Provider{blockedModels: map[string]time.Time{}}
	p.markModelBlocked(regionGlobal, "o4-mini")
	catalog := &reverse.ModelCatalog{Models: []reverse.Model{{ID: "o4-mini"}}}
	filtered := p.filterBlockedModels(regionGlobal, catalog)
	require.Len(t, filtered.Models, 1)
	require.Same(t, catalog, filtered)
}
