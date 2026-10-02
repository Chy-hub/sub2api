package service

import (
	"context"
	"time"
)

// GroupCapacitySummary holds aggregated capacity for a single group.
type GroupCapacitySummary struct {
	GroupID                   int64 `json:"group_id"`
	ConcurrencyUsed           int   `json:"concurrency_used"`
	ConcurrencyMax            int   `json:"concurrency_max"`
	ConcurrencyUsedIncomplete int   `json:"concurrency_used_incomplete_count"`
	ConcurrencyMaxIncomplete  int   `json:"concurrency_max_incomplete_count"`
	SessionsUsed              int   `json:"sessions_used"`
	SessionsMax               int   `json:"sessions_max"`
	RPMUsed                   int   `json:"rpm_used"`
	RPMMax                    int   `json:"rpm_max"`
	RPMUsedIncomplete         int   `json:"rpm_used_incomplete_count"`
	RPMMaxIncomplete          int   `json:"rpm_max_incomplete_count"`
}

// GroupAccountCapacityRow is the lightweight account projection needed for
// capacity summary aggregation.
type GroupAccountCapacityRow struct {
	GroupID             int64
	AccountID           int64
	Platform            string
	Type                string
	USTCScope           string // SHA-256 scope for one physical USTC Key; never the raw credential.
	Concurrency         int
	Extra               map[string]any
	SessionWindowStart  *time.Time
	SessionWindowEnd    *time.Time
	SessionWindowStatus string
}

type groupCapacityActiveGroupIDLister interface {
	ListActiveIDs(ctx context.Context) ([]int64, error)
}

type groupCapacityAccountLister interface {
	ListSchedulableCapacityByGroupIDs(ctx context.Context, groupIDs []int64) ([]GroupAccountCapacityRow, error)
}

// GroupCapacityService aggregates per-group capacity from runtime data.
type GroupCapacityService struct {
	accountRepo        AccountRepository
	groupRepo          GroupRepository
	concurrencyService *ConcurrencyService
	sessionLimitCache  SessionLimitCache
	rpmCache           RPMCache
}

// NewGroupCapacityService creates a new GroupCapacityService.
func NewGroupCapacityService(
	accountRepo AccountRepository,
	groupRepo GroupRepository,
	concurrencyService *ConcurrencyService,
	sessionLimitCache SessionLimitCache,
	rpmCache RPMCache,
) *GroupCapacityService {
	return &GroupCapacityService{
		accountRepo:        accountRepo,
		groupRepo:          groupRepo,
		concurrencyService: concurrencyService,
		sessionLimitCache:  sessionLimitCache,
		rpmCache:           rpmCache,
	}
}

// GetAllGroupCapacity returns capacity summary for all active groups.
func (s *GroupCapacityService) GetAllGroupCapacity(ctx context.Context) ([]GroupCapacitySummary, error) {
	groupIDs, err := s.listActiveGroupIDs(ctx)
	if err != nil {
		return nil, err
	}

	if lister, ok := s.accountRepo.(groupCapacityAccountLister); ok {
		return s.getGroupCapacitiesBatch(ctx, groupIDs, lister)
	}

	return s.getGroupCapacitiesSequential(ctx, groupIDs), nil
}

func (s *GroupCapacityService) listActiveGroupIDs(ctx context.Context) ([]int64, error) {
	if lister, ok := s.groupRepo.(groupCapacityActiveGroupIDLister); ok {
		return lister.ListActiveIDs(ctx)
	}

	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	groupIDs := make([]int64, 0, len(groups))
	for i := range groups {
		groupIDs = append(groupIDs, groups[i].ID)
	}
	return groupIDs, nil
}

func (s *GroupCapacityService) getGroupCapacitiesSequential(ctx context.Context, groupIDs []int64) []GroupCapacitySummary {
	rows := make([]GroupAccountCapacityRow, 0)
	loadedGroupIDs := make([]int64, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, groupID)
		if err != nil {
			// Skip groups with errors, return partial results
			continue
		}
		loadedGroupIDs = append(loadedGroupIDs, groupID)
		for i := range accounts {
			acc := &accounts[i]
			rows = append(rows, GroupAccountCapacityRow{
				GroupID:             groupID,
				AccountID:           acc.ID,
				Platform:            acc.Platform,
				Type:                acc.Type,
				USTCScope:           USTCKeyScope(acc),
				Concurrency:         acc.Concurrency,
				Extra:               acc.Extra,
				SessionWindowStart:  acc.SessionWindowStart,
				SessionWindowEnd:    acc.SessionWindowEnd,
				SessionWindowStatus: acc.SessionWindowStatus,
			})
		}
	}
	results, _ := s.aggregateGroupCapacityRows(ctx, loadedGroupIDs, rows)
	return results
}

type groupCapacityAccountRef struct {
	groupID   int64
	accountID int64
}

func (s *GroupCapacityService) getGroupCapacitiesBatch(ctx context.Context, groupIDs []int64, lister groupCapacityAccountLister) ([]GroupCapacitySummary, error) {
	rows, err := lister.ListSchedulableCapacityByGroupIDs(ctx, groupIDs)
	if err != nil {
		return nil, err
	}
	return s.aggregateGroupCapacityRows(ctx, groupIDs, rows)
}

type groupCapacityUSTCScope struct {
	limits USTCLimits
	known  bool
}

func (s *GroupCapacityService) aggregateGroupCapacityRows(ctx context.Context, groupIDs []int64, rows []GroupAccountCapacityRow) ([]GroupCapacitySummary, error) {
	results := make([]GroupCapacitySummary, len(groupIDs))
	groupIndex := make(map[int64]int, len(groupIDs))
	for i, groupID := range groupIDs {
		results[i].GroupID = groupID
		groupIndex[groupID] = i
	}
	if len(groupIDs) == 0 || len(rows) == 0 {
		return results, nil
	}

	refs := make([]groupCapacityAccountRef, 0, len(rows))
	seenGroupAccount := make(map[groupCapacityAccountRef]struct{}, len(rows))
	concurrencyAccountIDs := make([]int64, 0, len(rows))
	concurrencyAccountIDSet := make(map[int64]struct{}, len(rows))
	rpmAccountIDs := make([]int64, 0, len(rows))
	rpmAccountIDSet := make(map[int64]struct{}, len(rows))
	sessionAccountIDs := make([]int64, 0, len(rows))
	sessionAccountIDSet := make(map[int64]struct{}, len(rows))
	sessionTimeouts := make(map[int64]time.Duration)
	refRPMEligible := make(map[groupCapacityAccountRef]bool, len(rows))
	refUSTCScope := make(map[groupCapacityAccountRef]string, len(rows))
	groupUSTCScopes := make(map[int64]map[string]struct{})
	ustcScopes := make(map[string]groupCapacityUSTCScope)
	ustcScopeOrder := make([]string, 0)

	for _, row := range rows {
		idx, ok := groupIndex[row.GroupID]
		if !ok || row.AccountID <= 0 {
			continue
		}

		ref := groupCapacityAccountRef{groupID: row.GroupID, accountID: row.AccountID}
		if _, ok := seenGroupAccount[ref]; ok {
			continue
		}
		seenGroupAccount[ref] = struct{}{}
		refs = append(refs, ref)

		acc := Account{
			ID:                  row.AccountID,
			Platform:            row.Platform,
			Type:                row.Type,
			Concurrency:         row.Concurrency,
			Extra:               row.Extra,
			SessionWindowStart:  row.SessionWindowStart,
			SessionWindowEnd:    row.SessionWindowEnd,
			SessionWindowStatus: row.SessionWindowStatus,
		}

		if maxSessions := acc.GetMaxSessions(); maxSessions > 0 {
			results[idx].SessionsMax += maxSessions
			timeout := time.Duration(acc.GetSessionIdleTimeoutMinutes()) * time.Minute
			if timeout <= 0 {
				timeout = 5 * time.Minute
			}
			sessionTimeouts[acc.ID] = timeout
			if _, ok := sessionAccountIDSet[acc.ID]; !ok {
				sessionAccountIDSet[acc.ID] = struct{}{}
				sessionAccountIDs = append(sessionAccountIDs, acc.ID)
			}
		}

		if row.USTCScope != "" {
			refUSTCScope[ref] = row.USTCScope
			if groupUSTCScopes[row.GroupID] == nil {
				groupUSTCScopes[row.GroupID] = make(map[string]struct{})
			}
			groupUSTCScopes[row.GroupID][row.USTCScope] = struct{}{}
			limits, known := USTCAccountLimits(&acc)
			if existing, ok := ustcScopes[row.USTCScope]; !ok {
				ustcScopes[row.USTCScope] = groupCapacityUSTCScope{limits: limits, known: known}
				ustcScopeOrder = append(ustcScopeOrder, row.USTCScope)
			} else if !existing.known && known {
				existing.limits = limits
				existing.known = true
				ustcScopes[row.USTCScope] = existing
			}
			continue
		}

		if _, ok := concurrencyAccountIDSet[acc.ID]; !ok {
			concurrencyAccountIDSet[acc.ID] = struct{}{}
			concurrencyAccountIDs = append(concurrencyAccountIDs, acc.ID)
		}
		results[idx].ConcurrencyMax += acc.Concurrency

		// Blank platform is retained for older callers/tests that only provide
		// the historical projection. Real repository rows always include it.
		if acc.IsAnthropicOAuthOrSetupToken() || row.Platform == "" {
			if rpm := acc.GetBaseRPM(); rpm > 0 {
				refRPMEligible[ref] = true
				results[idx].RPMMax += rpm
				if _, ok := rpmAccountIDSet[acc.ID]; !ok {
					rpmAccountIDSet[acc.ID] = struct{}{}
					rpmAccountIDs = append(rpmAccountIDs, acc.ID)
				}
			}
		}
	}

	concurrencyMap := map[int64]int{}
	if s.concurrencyService != nil && len(concurrencyAccountIDs) > 0 {
		concurrencyMap, _ = s.concurrencyService.GetAccountConcurrencyBatch(ctx, concurrencyAccountIDs)
	}

	var sessionsMap map[int64]int
	if len(sessionAccountIDs) > 0 && s.sessionLimitCache != nil {
		sessionsMap, _ = s.sessionLimitCache.GetActiveSessionCountBatch(ctx, sessionAccountIDs, sessionTimeouts)
	}

	var rpmMap map[int64]int
	if len(rpmAccountIDs) > 0 && s.rpmCache != nil {
		rpmMap, _ = s.rpmCache.GetRPMBatch(ctx, rpmAccountIDs)
	}
	ustcCapacities := make(map[string]USTCCapacity)
	if capacityCache, ok := s.rpmCache.(USTCCapacityCache); ok {
		for _, scope := range ustcScopeOrder {
			state := ustcScopes[scope]
			if !state.known {
				continue
			}
			capacity, err := capacityCache.USTCRead(ctx, scope, state.limits)
			if err == nil {
				ustcCapacities[scope] = capacity
			}
		}
	}

	for _, ref := range refs {
		idx := groupIndex[ref.groupID]
		if refUSTCScope[ref] == "" {
			results[idx].ConcurrencyUsed += concurrencyMap[ref.accountID]
		}
		if sessionsMap != nil && results[idx].SessionsMax > 0 {
			results[idx].SessionsUsed += sessionsMap[ref.accountID]
		}
		if rpmMap != nil && refRPMEligible[ref] {
			results[idx].RPMUsed += rpmMap[ref.accountID]
		}
	}
	for groupID, scopes := range groupUSTCScopes {
		idx := groupIndex[groupID]
		for scope := range scopes {
			state := ustcScopes[scope]
			if !state.known {
				results[idx].RPMUsedIncomplete++
				results[idx].RPMMaxIncomplete++
				results[idx].ConcurrencyUsedIncomplete++
				results[idx].ConcurrencyMaxIncomplete++
				continue
			}

			rpmMax := state.limits.RPM
			parallelMax := state.limits.Parallel
			capacity, ok := ustcCapacities[scope]
			if !ok {
				results[idx].RPMUsedIncomplete++
				results[idx].ConcurrencyUsedIncomplete++
			} else {
				if capacity.RPMLimit != nil {
					rpmMax = *capacity.RPMLimit
				}
				if capacity.ParallelLimit != nil {
					parallelMax = *capacity.ParallelLimit
				}
				results[idx].RPMUsed += capacity.Used + capacity.Pending
				results[idx].ConcurrencyUsed += capacity.InFlight
			}
			results[idx].RPMMax += rpmMax
			results[idx].ConcurrencyMax += parallelMax
		}
	}
	return results, nil
}
