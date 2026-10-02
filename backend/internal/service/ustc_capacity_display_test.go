package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcCapacityDisplayCacheStub struct {
	RPMCache
	USTCCapacityCache

	mu         sync.Mutex
	calls      map[string]int
	active     int
	maxActive  int
	errorScope map[string]error
}

func (s *ustcCapacityDisplayCacheStub) USTCRead(_ context.Context, scope string, limits USTCLimits) (USTCCapacity, error) {
	s.mu.Lock()
	s.calls[scope]++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	err := s.errorScope[scope]
	s.mu.Unlock()

	// Keep reads in flight long enough to exercise the batch concurrency bound.
	time.Sleep(10 * time.Millisecond)

	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	if err != nil {
		return USTCCapacity{}, err
	}
	rpm, parallel := limits.RPM, limits.Parallel
	return USTCCapacity{RPMLimit: &rpm, ParallelLimit: &parallel, Used: 3, InFlight: 1, Available: 2, State: "ready"}, nil
}

func ustcDisplayAccount(id int64, key, accountType, baseURL string, extra map[string]any) Account {
	return Account{
		ID:       id,
		Platform: PlatformOpenAI,
		Type:     accountType,
		Credentials: map[string]any{
			"api_key":  key,
			"base_url": baseURL,
		},
		Extra: extra,
	}
}

func TestUSTCAccountCapacitiesBatchDeduplicatesAndBoundsReads(t *testing.T) {
	known := map[string]any{
		UserInfoQuotaExtraKey("limits_known"):          true,
		UserInfoQuotaExtraKey("rpm_limit"):             20,
		UserInfoQuotaExtraKey("max_parallel_requests"): 4,
	}
	accounts := make([]Account, 0, 20)
	for i := int64(1); i <= 16; i++ {
		accounts = append(accounts, ustcDisplayAccount(i, fmt.Sprintf("key-%d", i), AccountTypeAPIKey, "https://api.llm.ustc.edu.cn/v1", known))
	}
	accounts = append(accounts,
		ustcDisplayAccount(100, "key-1", AccountTypeAPIKey, "https://api.llm.ustc.edu.cn/v1", known),
		ustcDisplayAccount(104, "key-2", AccountTypeAPIKey, "https://api.llm.ustc.edu.cn/v1", map[string]any{"base_rpm": 99}),
		ustcDisplayAccount(101, "unknown-key", AccountTypeAPIKey, "https://api.llm.ustc.edu.cn/v1", map[string]any{"base_rpm": 99}),
		ustcDisplayAccount(102, "upstream-key", AccountTypeUpstream, "https://api.llm.ustc.edu.cn/v1", known),
		ustcDisplayAccount(103, "regular-key", AccountTypeAPIKey, "https://api.openai.com/v1", known),
	)
	cache := &ustcCapacityDisplayCacheStub{calls: make(map[string]int), errorScope: make(map[string]error)}
	cache.errorScope[USTCKeyScope(&accounts[1])] = errors.New("redis unavailable")

	capacities := USTCAccountCapacitiesBatch(context.Background(), cache, accounts)

	require.Len(t, capacities, 19, "all USTC API-key accounts get a response, including unknown metadata")
	require.Equal(t, 3, capacities[1].Used)
	require.True(t, capacities[1].LimitsKnown)
	require.True(t, capacities[1].CountsKnown)
	require.True(t, capacities[100].CountsKnown, "duplicate rows for one physical key share the successful snapshot")
	require.True(t, capacities[2].LimitsKnown)
	require.False(t, capacities[2].CountsKnown, "a failed Redis read must not masquerade as zero usage")
	require.True(t, capacities[104].LimitsKnown, "a duplicate account should share limits known for its physical key")
	require.False(t, capacities[104].CountsKnown)
	require.NotNil(t, capacities[104].RPMLimit)
	require.Equal(t, 20, *capacities[104].RPMLimit)
	require.False(t, capacities[101].LimitsKnown)
	require.False(t, capacities[101].CountsKnown)
	require.NotContains(t, capacities, int64(102), "upstream quota visibility does not opt into automated capacity")
	require.NotContains(t, capacities, int64(103), "non-USTC hosts do not opt into automated capacity")

	cache.mu.Lock()
	defer cache.mu.Unlock()
	require.Len(t, cache.calls, 16, "duplicate physical keys are read once and unknown limits are not read")
	require.Equal(t, 1, cache.calls[USTCKeyScope(&accounts[0])])
	require.LessOrEqual(t, cache.maxActive, 8, "Redis reads stay within the bounded worker limit")
	require.Greater(t, cache.maxActive, 1, "distinct keys are read concurrently")
}
