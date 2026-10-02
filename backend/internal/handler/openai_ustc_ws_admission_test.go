//go:build unit

package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIUSTCWebSocketBridgeReusesFirstTicketAndCountsEveryTurn(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := repository.NewRPMCache(client)
	capacityCache := cache.(service.USTCCapacityCache)
	account := &service.Account{Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "sk-test"}}
	scope := service.USTCKeyScope(account)
	limits := service.USTCLimits{RPM: 20, Parallel: 1}
	ctx := context.Background()
	// Give the Key a trusted non-stream anchor so SSE's absent headers are not
	// mistaken for a cold-window recovery test. The anchor costs one RPM.
	anchor, _, err := capacityCache.USTCReserve(ctx, scope, limits)
	require.NoError(t, err)
	require.NotNil(t, anchor)
	allowed, err := capacityCache.USTCCommit(ctx, anchor, limits)
	require.NoError(t, err)
	require.True(t, allowed)
	remaining := 19
	require.NoError(t, capacityCache.USTCObserve(ctx, anchor, service.USTCFeedback{StatusCode: 200, RPMLimit: &limits.RPM, Remaining: &remaining}))
	require.NoError(t, capacityCache.USTCRelease(ctx, anchor))

	result := runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
		ustcCapacityCache: cache,
		firstPayload:      `{"type":"response.create","model":"deepseek-flash","input":"hello"}`,
		secondPayload:     `{"type":"response.create","model":"deepseek-flash","input":"again"}`,
		ingressMode:       service.OpenAIWSIngressModeCtxPool,
	})
	require.Len(t, result.clientEvents, 2)
	require.Len(t, result.upstreamPayloads, 2)
	require.Eventually(t, func() bool {
		capacity, err := capacityCache.USTCRead(ctx, scope, limits)
		return err == nil && capacity.InFlight == 0
	}, time.Second, time.Millisecond)
	capacity, err := capacityCache.USTCRead(ctx, scope, limits)
	require.NoError(t, err)
	require.Equal(t, 3, capacity.Used, "one anchor plus two real bridge turns, no duplicate RPM commits")
	require.Zero(t, capacity.InFlight)
	require.Zero(t, capacity.Pending)

	// With a single parallel slot, a second Reserve before the first turn's
	// send would reject it. Both completed turns therefore also prove ticket
	// context propagation and body-close release, not just the final counters.
}
