package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"golang.org/x/sync/singleflight"
)

const (
	ustcPoolMembershipTTL = 2 * time.Second
	ustcPoolMembershipMax = 512
	ustcPoolPollInterval  = 200 * time.Millisecond
)

type USTCAccountPoolRepository interface {
	ListUSTCAccountPool(context.Context, *int64, bool) ([]Account, error)
}

// Waiting permits are independent of generation slots and shared by instances.
type USTCPoolWaitCache interface {
	AcquireUSTCPoolWait(context.Context, string, int, time.Duration) (bool, func(), error)
}

type ustcPoolMembership struct {
	accounts []Account
	until    time.Time
	err      error
}

type ustcAccountPoolState struct {
	mu      sync.Mutex
	members map[string]ustcPoolMembership
	waiters map[string]int
	polls   map[string]ustcPoolPollSnapshot
	flight  singleflight.Group
}

type ustcPoolPollKey struct{}

type ustcPoolPollSnapshot struct {
	ids    map[int64]struct{}
	counts map[int64]int
	loads  map[int64]*AccountLoadInfo
	until  time.Time
}

func (s *OpenAIGatewayService) defaultUSTCPoolState() *ustcAccountPoolState {
	s.ustcPoolOnce.Do(func() {
		s.ustcPool = &ustcAccountPoolState{members: make(map[string]ustcPoolMembership), waiters: make(map[string]int), polls: make(map[string]ustcPoolPollSnapshot)}
	})
	return s.ustcPool
}

func (s *OpenAIGatewayService) defaultUSTCPoolScope(groupID *int64) (string, *int64, bool) {
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return "simple", nil, true
	}
	return "group_" + strconv.FormatInt(derefGroupID(groupID), 10), groupID, false
}

func cloneUSTCPool(accounts []Account) []Account {
	cloned := make([]Account, len(accounts))
	for i := range accounts {
		cloned[i] = *copyAccountForUserInfoQuotaRefresh(&accounts[i])
	}
	return cloned
}

// Only USTC members are supplemented. Other upstreams retain the normal pool.
func (s *OpenAIGatewayService) supplementDefaultUSTCPool(ctx context.Context, groupID *int64, accounts []Account) []Account {
	repo, ok := s.accountRepo.(USTCAccountPoolRepository)
	if !ok {
		return accounts
	}
	key, queryGroup, includeGrouped := s.defaultUSTCPoolScope(groupID)
	state := s.defaultUSTCPoolState()
	read := func() (ustcPoolMembership, bool) {
		state.mu.Lock()
		defer state.mu.Unlock()
		entry, found := state.members[key]
		return entry, found && time.Now().Before(entry.until)
	}
	entry, hit := read()
	if !hit {
		resultCh := state.flight.DoChan(key, func() (any, error) {
			if cached, ok := read(); ok {
				return cached, nil
			}
			queryCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			pool, err := repo.ListUSTCAccountPool(queryCtx, queryGroup, includeGrouped)
			entry := ustcPoolMembership{accounts: cloneUSTCPool(pool), until: time.Now().Add(ustcPoolMembershipTTL), err: err}
			state.mu.Lock()
			if len(state.members) >= ustcPoolMembershipMax {
				for oldKey, old := range state.members {
					if !time.Now().Before(old.until) {
						delete(state.members, oldKey)
					}
				}
				if len(state.members) >= ustcPoolMembershipMax {
					for oldKey := range state.members {
						delete(state.members, oldKey)
						break
					}
				}
			}
			state.members[key] = entry
			state.mu.Unlock()
			if err != nil {
				slog.Warn("ustc_pool_membership_refresh_failed", "pool", key, "error", err)
			}
			return entry, nil
		})
		select {
		case <-ctx.Done():
			return accounts
		case result := <-resultCh:
			if result.Err != nil {
				return accounts
			}
			entry = result.Val.(ustcPoolMembership)
		}
	}
	if entry.err != nil {
		return accounts
	}
	merged := make([]Account, 0, len(accounts)+len(entry.accounts))
	for i := range accounts {
		if !isDefaultUSTCAccount(&accounts[i]) {
			merged = append(merged, accounts[i])
		}
	}
	return append(merged, cloneUSTCPool(entry.accounts)...)
}

type ustcPoolCapacityError struct {
	cause      error
	retryAfter time.Duration
}

func (e *ustcPoolCapacityError) Error() string { return e.cause.Error() }
func (e *ustcPoolCapacityError) Unwrap() error { return e.cause }

// USTCPoolRetryAfter identifies this branch's capacity response without changing
// classification for any other upstream.
func USTCPoolRetryAfter(err error) (int, bool) {
	var capacity *ustcPoolCapacityError
	if !errors.As(err, &capacity) {
		return 0, false
	}
	return max(1, int(math.Ceil(capacity.retryAfter.Seconds()))), true
}

func (s *OpenAIGatewayService) acquireDefaultUSTCPoolWait(ctx context.Context, groupID *int64, maximum int, ttl time.Duration) (bool, func()) {
	key, _, _ := s.defaultUSTCPoolScope(groupID)
	if cache, ok := s.rpmCache.(USTCPoolWaitCache); ok {
		allowed, release, err := cache.AcquireUSTCPoolWait(ctx, key, maximum, ttl)
		if err != nil {
			slog.Warn("ustc_pool_wait_admission_failed", "pool", key, "error", err)
			return false, nil
		}
		return allowed, release
	}
	state := s.defaultUSTCPoolState()
	state.mu.Lock()
	if maximum <= 0 || state.waiters[key] >= maximum {
		state.mu.Unlock()
		return false, nil
	}
	state.waiters[key]++
	state.mu.Unlock()
	var once sync.Once
	return true, func() {
		once.Do(func() {
			state.mu.Lock()
			state.waiters[key]--
			if state.waiters[key] == 0 {
				delete(state.waiters, key)
			}
			state.mu.Unlock()
		})
	}
}

// Requests wait across the pool, never on one USTC account. No generation slot
// or RPM reservation is held while waiting for an account to recover.
func (s *OpenAIGatewayService) selectBalancedDefaultUSTCAccountWithWait(ctx context.Context, groupID *int64, accounts []Account, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requireCompact bool, capability OpenAIEndpointCapability, preferLowRate bool,
) (*AccountSelectionResult, error) {
	cfg := s.schedulingConfig()
	selectionCtx := ctx
	var waitDeadline time.Time
	var lastCapacity error
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !waitDeadline.IsZero() && !time.Now().Before(waitDeadline) {
			return nil, lastCapacity
		}
		selection, err := s.selectBalancedDefaultUSTCAccount(selectionCtx, groupID, accounts, sessionHash, requestedModel, excludedIDs, requireCompact, capability, preferLowRate)
		if !waitDeadline.IsZero() && selectionCtx.Err() != nil && ctx.Err() == nil {
			if selection != nil && selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			return nil, lastCapacity
		}
		if selection != nil && selection.WaitPlan != nil && isDefaultUSTCAccount(selection.Account) && cfg.FallbackWaitTimeout > 0 {
			err = &ustcPoolCapacityError{cause: noAvailableOpenAISelectionError(requestedModel, false, "ustc_pool_busy"), retryAfter: time.Second}
			selection = nil
		}
		var capacity *ustcPoolCapacityError
		if !errors.As(err, &capacity) {
			return selection, err
		}
		lastCapacity = err
		if waitDeadline.IsZero() {
			if cfg.FallbackWaitTimeout <= 0 || cfg.FallbackMaxWaiting <= 0 {
				return nil, err
			}
			waitDeadline = time.Now().Add(cfg.FallbackWaitTimeout)
			allowed, cancel := s.acquireDefaultUSTCPoolWait(ctx, groupID, cfg.FallbackMaxWaiting, cfg.FallbackWaitTimeout+2*time.Second)
			if !allowed {
				return nil, &ustcPoolCapacityError{cause: fmt.Errorf("%w (ustc_pool_wait_full)", err), retryAfter: capacity.retryAfter}
			}
			release = cancel
			var deadlineCancel context.CancelFunc
			selectionCtx, deadlineCancel = context.WithDeadline(ctx, waitDeadline)
			key, _, _ := s.defaultUSTCPoolScope(groupID)
			selectionCtx = context.WithValue(selectionCtx, ustcPoolPollKey{}, key)
			defer deadlineCancel()
		}
		if !time.Now().Before(waitDeadline) {
			return nil, lastCapacity
		}
		// Poll all kinds of recovery, including RPM refunds and freed slots. A
		// minute-long sleep based only on RPM would leave recovered capacity idle.
		pause := min(ustcPoolPollInterval, time.Until(waitDeadline))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		accounts = s.supplementDefaultUSTCPool(selectionCtx, groupID, accounts)
	}
}

// Waiters share read-only load/RPM observations briefly. Atomic reservations
// remain authoritative; this avoids a Redis batch for every queued request on
// every poll without holding back newly recovered capacity for a whole window.
func (s *OpenAIGatewayService) defaultUSTCPoolPoll(ctx context.Context, ids []int64, candidates []*Account) (map[int64]int, map[int64]*AccountLoadInfo) {
	// A shared read can outlive a canceled caller, which may then reorder its
	// candidate slice. Keep the async callback's inputs independent of that slice.
	ids = append([]int64(nil), ids...)
	candidates = append([]*Account(nil), candidates...)
	readUpstream := func(readCtx context.Context) ustcPoolPollSnapshot {
		entry := ustcPoolPollSnapshot{ids: make(map[int64]struct{}, len(candidates)), counts: map[int64]int{}, loads: map[int64]*AccountLoadInfo{}}
		for _, account := range candidates {
			entry.ids[account.ID] = struct{}{}
		}
		if s.rpmCache != nil && len(ids) > 0 {
			if counts, err := s.rpmCache.GetRPMBatch(readCtx, ids); err == nil {
				entry.counts = counts
			}
		}
		if s.concurrencyService != nil && s.schedulingConfig().LoadBatchEnabled {
			if loads, err := s.concurrencyService.GetAccountsLoadBatch(readCtx, buildOpenAIAccountLoadRequest(candidates)); err == nil {
				entry.loads = loads
			}
		}
		entry.until = time.Now().Add(100 * time.Millisecond)
		return entry
	}
	key, waiting := ctx.Value(ustcPoolPollKey{}).(string)
	if !waiting {
		entry := readUpstream(ctx)
		return entry.counts, entry.loads
	}
	state := s.defaultUSTCPoolState()
	read := func() (ustcPoolPollSnapshot, bool) {
		state.mu.Lock()
		defer state.mu.Unlock()
		entry, ok := state.polls[key]
		if !ok || !time.Now().Before(entry.until) {
			return entry, false
		}
		for _, candidate := range candidates {
			if _, found := entry.ids[candidate.ID]; !found {
				return entry, false
			}
		}
		return entry, true
	}
	entry, hit := read()
	if !hit {
		resultCh := state.flight.DoChan("poll:"+key, func() (any, error) {
			if cached, ok := read(); ok {
				return cached, nil
			}
			readCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			entry := readUpstream(readCtx)
			state.mu.Lock()
			// This cache is bounded independently of membership, including test
			// and plugin repositories without the dedicated pool interface.
			if len(state.polls) >= ustcPoolMembershipMax {
				for oldKey := range state.polls {
					delete(state.polls, oldKey)
					break
				}
			}
			state.polls[key] = entry
			state.mu.Unlock()
			return entry, nil
		})
		select {
		case <-ctx.Done():
			return nil, nil
		case result := <-resultCh:
			entry = result.Val.(ustcPoolPollSnapshot)
		}
	}
	return entry.counts, entry.loads
}

func ustcAccountCoolingUntil(account *Account, now time.Time) time.Time {
	var until time.Time
	for _, at := range []*time.Time{account.RateLimitResetAt, account.OverloadUntil, account.TempUnschedulableUntil} {
		if at != nil && at.After(now) && at.After(until) {
			until = *at
		}
	}
	return until
}

func ustcQuotaNextCheck(account *Account, now time.Time) time.Time {
	// A quota refresh may discover an earlier reset or newly available budget.
	// Do not make a waiter sleep until a stale multi-hour window expires.
	updated, known := userInfoQuotaExtraUpdatedAt(account.Extra)
	next := now.Add(userInfoQuotaSchedulingFreshness)
	if known && updated.Add(userInfoQuotaSchedulingFreshness).After(now) {
		next = updated.Add(userInfoQuotaSchedulingFreshness)
	}
	for _, window := range userInfoQuotaWindowsForScheduling(account.Extra) {
		if userInfoQuotaWindowExhausted(window) && window.ResetAt != "" {
			if at, err := parseUserInfoTime(window.ResetAt); err == nil && at.After(now) && at.Before(next) {
				next = at
			}
		}
	}
	return next
}
