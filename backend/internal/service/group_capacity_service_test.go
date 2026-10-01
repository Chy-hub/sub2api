package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type groupCapacityAccountRepoStub struct {
	AccountRepository
	rows      []GroupAccountCapacityRow
	requested []int64
}

func (s *groupCapacityAccountRepoStub) ListSchedulableCapacityByGroupIDs(_ context.Context, groupIDs []int64) ([]GroupAccountCapacityRow, error) {
	s.requested = append([]int64(nil), groupIDs...)
	return append([]GroupAccountCapacityRow(nil), s.rows...), nil
}

type groupCapacityGroupRepoStub struct {
	GroupRepository
	groupIDs  []int64
	listCalls int
}

func (s *groupCapacityGroupRepoStub) ListActiveIDs(context.Context) ([]int64, error) {
	s.listCalls++
	return append([]int64(nil), s.groupIDs...), nil
}

type groupCapacityConcurrencyCacheStub struct {
	ConcurrencyCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityConcurrencyCacheStub) GetAccountConcurrencyBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacitySessionCacheStub struct {
	SessionLimitCache
	counts       map[int64]int
	requested    []int64
	idleTimeouts map[int64]time.Duration
}

func (s *groupCapacitySessionCacheStub) GetActiveSessionCountBatch(_ context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	s.idleTimeouts = make(map[int64]time.Duration, len(idleTimeouts))
	for id, timeout := range idleTimeouts {
		s.idleTimeouts[id] = timeout
	}
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacityRPMCacheStub struct {
	RPMCache
	counts    map[int64]int
	requested []int64
}

func (s *groupCapacityRPMCacheStub) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.requested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

type groupCapacitySharedRPMCacheStub struct {
	RPMCache
	USTCCapacityCache
	counts       map[int64]int
	rpmRequested []int64
	ustcReads    []string
	ustcLimits   []USTCLimits
	capacity     USTCCapacity
}

func (s *groupCapacitySharedRPMCacheStub) GetRPMBatch(_ context.Context, accountIDs []int64) (map[int64]int, error) {
	s.rpmRequested = append([]int64(nil), accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

func (s *groupCapacitySharedRPMCacheStub) USTCRead(_ context.Context, scope string, limits USTCLimits) (USTCCapacity, error) {
	s.ustcReads = append(s.ustcReads, scope)
	s.ustcLimits = append(s.ustcLimits, limits)
	return s.capacity, nil
}

type groupCapacitySequentialAccountRepoStub struct {
	AccountRepository
	accountsByGroup map[int64][]Account
	requested       []int64
}

func (s *groupCapacitySequentialAccountRepoStub) ListSchedulableByGroupID(_ context.Context, groupID int64) ([]Account, error) {
	s.requested = append(s.requested, groupID)
	return append([]Account(nil), s.accountsByGroup[groupID]...), nil
}

func knownUSTCCapacityExtra(rpm, parallel int) map[string]any {
	return map[string]any{
		"base_rpm":                                     10,
		UserInfoQuotaExtraKey("limits_known"):          true,
		UserInfoQuotaExtraKey("rpm_limit"):             rpm,
		UserInfoQuotaExtraKey("max_parallel_requests"): parallel,
	}
}

func TestGetAllGroupCapacityBatchAggregatesRuntimeAndLimits(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{
				GroupID:     10,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   1,
				Concurrency: 2,
				Extra: map[string]any{
					"max_sessions":                 3,
					"session_idle_timeout_minutes": 7,
					"base_rpm":                     11,
				},
			},
			{
				GroupID:     20,
				AccountID:   2,
				Concurrency: 4,
				Extra: map[string]any{
					"max_sessions":                 1,
					"session_idle_timeout_minutes": 9,
					"base_rpm":                     13,
				},
			},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 5, 2: 7}}
	svc := NewGroupCapacityService(
		accountRepo,
		groupRepo,
		NewConcurrencyService(concurrencyCache),
		sessionCache,
		rpmCache,
	)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, 1, groupRepo.listCalls)
	require.Equal(t, []int64{10, 20}, accountRepo.requested)
	require.Equal(t, []int64{1, 2}, concurrencyCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, sessionCache.requested)
	require.ElementsMatch(t, []int64{1, 2}, rpmCache.requested)
	require.Equal(t, 7*time.Minute, sessionCache.idleTimeouts[1])
	require.Equal(t, 9*time.Minute, sessionCache.idleTimeouts[2])

	require.Equal(t, []GroupCapacitySummary{
		{
			GroupID:         10,
			ConcurrencyUsed: 1,
			ConcurrencyMax:  2,
			SessionsUsed:    2,
			SessionsMax:     3,
			RPMUsed:         5,
			RPMMax:          11,
		},
		{
			GroupID:         20,
			ConcurrencyUsed: 3,
			ConcurrencyMax:  6,
			SessionsUsed:    3,
			SessionsMax:     4,
			RPMUsed:         12,
			RPMMax:          24,
		},
	}, results)
}

func TestGetAllGroupCapacityBatchKeepsEmptyGroupRows(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{GroupID: 20, AccountID: 2, Concurrency: 4},
		},
	}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, nil)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)

	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10},
		{GroupID: 20, ConcurrencyMax: 4},
	}, results)
}

func TestGetAllGroupCapacityUsesSharedUSTCLimitsOncePerScope(t *testing.T) {
	const scope = "ustc-key-fingerprint"
	accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{
		{GroupID: 10, AccountID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, USTCScope: scope, Concurrency: 10, Extra: knownUSTCCapacityExtra(20, 20)},
		{GroupID: 10, AccountID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, USTCScope: scope, Concurrency: 10, Extra: knownUSTCCapacityExtra(20, 20)},
		{GroupID: 20, AccountID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, USTCScope: scope, Concurrency: 10, Extra: knownUSTCCapacityExtra(20, 20)},
	}}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 8, 2: 8, 3: 8}}
	rpmCache := &groupCapacitySharedRPMCacheStub{
		counts:   map[int64]int{1: 99, 2: 99, 3: 99},
		capacity: USTCCapacity{Used: 6, Pending: 2, InFlight: 3},
	}
	svc := NewGroupCapacityService(accountRepo, groupRepo, NewConcurrencyService(concurrencyCache), nil, rpmCache)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10, RPMUsed: 8, RPMMax: 20, ConcurrencyUsed: 3, ConcurrencyMax: 20},
		{GroupID: 20, RPMUsed: 8, RPMMax: 20, ConcurrencyUsed: 3, ConcurrencyMax: 20},
	}, results)
	require.Equal(t, []string{scope}, rpmCache.ustcReads, "one shared key must be read once across groups")
	require.Equal(t, []USTCLimits{{RPM: 20, Parallel: 20}}, rpmCache.ustcLimits, "auto limits must override legacy base_rpm")
	require.Empty(t, rpmCache.rpmRequested, "USTC must not use the local minute RPM counter")
	require.Empty(t, concurrencyCache.requested, "USTC must not use account-local concurrency")
}

func TestGetAllGroupCapacityIgnoresOpenAIManualRPMAndKeepsAnthropicRPM(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{
		{GroupID: 10, AccountID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 2, Extra: map[string]any{"base_rpm": 12}},
		{GroupID: 10, AccountID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 3, Extra: map[string]any{"base_rpm": 50}},
	}}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10}}
	concurrencyCache := &groupCapacityConcurrencyCacheStub{counts: map[int64]int{1: 1, 2: 2}}
	rpmCache := &groupCapacityRPMCacheStub{counts: map[int64]int{1: 4, 2: 40}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, NewConcurrencyService(concurrencyCache), nil, rpmCache)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{{
		GroupID: 10, ConcurrencyUsed: 3, ConcurrencyMax: 5, RPMUsed: 4, RPMMax: 12,
	}}, results)
	require.Equal(t, []int64{1}, rpmCache.requested)
}

func TestGetAllGroupCapacityDoesNotFallbackToManualRPMForUnknownUSTCLimits(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{rows: []GroupAccountCapacityRow{{
		GroupID: 10, AccountID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		USTCScope: "unknown-limits-key", Concurrency: 10,
		Extra: map[string]any{"base_rpm": 100, UserInfoQuotaExtraKey("limits_known"): false},
	}}}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10}}
	rpmCache := &groupCapacitySharedRPMCacheStub{}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, rpmCache)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{{GroupID: 10}}, results)
	require.Empty(t, rpmCache.ustcReads)
	require.Empty(t, rpmCache.rpmRequested)
}

func TestGetAllGroupCapacitySequentialSharesUSTCReadAcrossGroups(t *testing.T) {
	const key = "shared-upstream-key"
	makeAccount := func(id int64) Account {
		return Account{
			ID:       id,
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"base_url": "https://api.llm.ustc.edu.cn",
				"api_key":  key,
			},
			Concurrency: 10,
			Extra:       knownUSTCCapacityExtra(20, 20),
		}
	}
	accountRepo := &groupCapacitySequentialAccountRepoStub{accountsByGroup: map[int64][]Account{
		10: {makeAccount(1), makeAccount(2)},
		20: {makeAccount(3)},
	}}
	groupRepo := &groupCapacityGroupRepoStub{groupIDs: []int64{10, 20}}
	rpmCache := &groupCapacitySharedRPMCacheStub{capacity: USTCCapacity{Used: 5, Pending: 1, InFlight: 2}}
	svc := NewGroupCapacityService(accountRepo, groupRepo, nil, nil, rpmCache)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Equal(t, []GroupCapacitySummary{
		{GroupID: 10, RPMUsed: 6, RPMMax: 20, ConcurrencyUsed: 2, ConcurrencyMax: 20},
		{GroupID: 20, RPMUsed: 6, RPMMax: 20, ConcurrencyUsed: 2, ConcurrencyMax: 20},
	}, results)
	require.Equal(t, []int64{10, 20}, accountRepo.requested)
	require.Len(t, rpmCache.ustcReads, 1)
	require.Equal(t, USTCKeyScope(&Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": key},
	}), rpmCache.ustcReads[0])
}
