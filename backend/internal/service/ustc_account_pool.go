package service

import (
	"context"
	"errors"
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
	mu           sync.Mutex
	members      map[string]ustcPoolMembership
	waiters      map[string]int
	coordinators map[string]*ustcPoolCoordinator
	cursor       uint64
	flight       singleflight.Group
}

func (s *OpenAIGatewayService) defaultUSTCPoolState() *ustcAccountPoolState {
	s.ustcPoolOnce.Do(func() {
		s.ustcPool = &ustcAccountPoolState{members: make(map[string]ustcPoolMembership), waiters: make(map[string]int), coordinators: make(map[string]*ustcPoolCoordinator)}
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
	if bound, _ := ctx.Value(ustcBoundAccountKey{}).(bool); bound {
		return accounts
	}
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
	mutable    bool
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
