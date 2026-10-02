package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// USTCCapacity observes one physical upstream Key. Null limits mean unlimited
// only for known metadata; state "unknown" never implies unlimited capacity.
type USTCCapacity struct {
	RPMLimit      *int      `json:"rpm_limit"`
	ParallelLimit *int      `json:"parallel_limit"`
	LimitsKnown   bool      `json:"limits_known"`
	CountsKnown   bool      `json:"counts_known"`
	Used          int       `json:"used"`
	InFlight      int       `json:"in_flight"`
	Available     int       `json:"available"`
	ResetAt       time.Time `json:"reset_at,omitzero"`
	State         string    `json:"state"`
	Pending       int       `json:"-"`
}

type USTCLimits struct {
	RPM      int // zero means explicitly unlimited, never unknown
	Parallel int
}

type USTCTicket struct {
	Scope string
	ID    string
	Epoch int64
	Probe bool
	Cold  bool // missing shared state; a stream may need one non-stream calibration
}

type USTCFeedback struct {
	StatusCode int
	Remaining  *int // missing headers, especially SSE, are not unlimited
	RPMLimit   *int
	RetryAfter time.Duration
	Refund     bool // only a proven pre-RPM denial, e.g. key_model_access_denied
}

// USTCCapacityCache uses Redis server time for every transition. Reserve holds
// one parallel lease and a refundable pending RPM. Commit revalidates the epoch
// immediately before sending and starts the 60s window. Release never refunds a
// committed RPM; concurrency survives window rollover until body close/release.
type USTCCapacityCache interface {
	USTCRead(context.Context, string, USTCLimits) (USTCCapacity, error)
	USTCReserve(context.Context, string, USTCLimits) (*USTCTicket, USTCCapacity, error)
	USTCCommit(context.Context, *USTCTicket, USTCLimits) (bool, error)
	USTCRelease(context.Context, *USTCTicket) error
	USTCRenew(context.Context, *USTCTicket) error
	USTCObserve(context.Context, *USTCTicket, USTCFeedback) error
	USTCCooldown(context.Context, string, time.Duration) error
}

func USTCKeyScope(account *Account) string {
	if !IsUSTCCapacityAccount(account) {
		return ""
	}
	key := strings.TrimSpace(account.GetCredential("api_key"))
	if key == "" {
		return ""
	}
	fingerprint := sha256.Sum256([]byte(key))
	return hex.EncodeToString(fingerprint[:])
}

// IsUSTCCapacityAccount is the narrower USTC Key-capacity policy. UserInfo
// quota observations can also apply to upstream accounts, but automated RPM
// and concurrency admission is only for OpenAI API-key accounts.
func IsUSTCCapacityAccount(account *Account) bool {
	return account != nil && account.IsOpenAI() && account.Type == AccountTypeAPIKey && account.SupportsUserInfoQuota()
}

func USTCAccountLimits(account *Account) (USTCLimits, bool) {
	if account == nil {
		return USTCLimits{}, false
	}
	known, _ := account.Extra[UserInfoQuotaExtraKey("limits_known")].(bool)
	if !known {
		return USTCLimits{}, false
	}
	read := func(suffix string) (int, bool) {
		key := UserInfoQuotaExtraKey(suffix)
		raw, exists := account.Extra[key]
		if !exists {
			return 0, false
		}
		if raw == nil {
			return 0, true
		}
		value, valid := resolveAccountExtraNumber(account.Extra, key)
		if !valid || value <= 0 || math.Trunc(value) != value || value > math.MaxInt32 {
			return 0, false
		}
		return int(value), true
	}
	rpm, rpmKnown := read("rpm_limit")
	parallel, parallelKnown := read("max_parallel_requests")
	return USTCLimits{RPM: rpm, Parallel: parallel}, rpmKnown && parallelKnown
}

type ustcCapacityDisplayScope struct {
	limits     USTCLimits
	known      bool
	accountIDs []int64
}

func ustcCapacityWithLimits(limits USTCLimits, known bool) *USTCCapacity {
	capacity := &USTCCapacity{State: "unknown", LimitsKnown: known}
	if known && limits.RPM > 0 {
		capacity.RPMLimit = &limits.RPM
	}
	if known && limits.Parallel > 0 {
		capacity.ParallelLimit = &limits.Parallel
	}
	return capacity
}

// USTCAccountCapacitiesBatch reads one snapshot per physical Key, with bounded
// parallelism so account lists do not serialize a Redis round trip per row.
// The runtime cache interface remains read-only and unchanged.
func USTCAccountCapacitiesBatch(ctx context.Context, cache RPMCache, accounts []Account) map[int64]*USTCCapacity {
	capacities := make(map[int64]*USTCCapacity)
	scopes := make(map[string]ustcCapacityDisplayScope)
	scopeOrder := make([]string, 0)
	for i := range accounts {
		account := &accounts[i]
		if !IsUSTCCapacityAccount(account) {
			continue
		}
		limits, known := USTCAccountLimits(account)
		capacities[account.ID] = ustcCapacityWithLimits(limits, known)
		scope := USTCKeyScope(account)
		if scope == "" {
			continue
		}
		state, exists := scopes[scope]
		if !exists {
			state = ustcCapacityDisplayScope{limits: limits, known: known}
			scopeOrder = append(scopeOrder, scope)
		} else if !state.known && known {
			state.limits = limits
			state.known = true
		}
		state.accountIDs = append(state.accountIDs, account.ID)
		scopes[scope] = state
	}
	for _, state := range scopes {
		capacity := ustcCapacityWithLimits(state.limits, state.known)
		for _, accountID := range state.accountIDs {
			copy := *capacity
			capacities[accountID] = &copy
		}
	}
	store, ok := cache.(USTCCapacityCache)
	if !ok {
		return capacities
	}

	type readResult struct {
		capacity USTCCapacity
		ok       bool
	}
	readResults := make([]readResult, len(scopeOrder))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, scope := range scopeOrder {
		state := scopes[scope]
		if !state.known {
			continue
		}
		i, scope := i, scope
		g.Go(func() error {
			observed, err := store.USTCRead(gctx, scope, state.limits)
			if err == nil {
				observed.LimitsKnown = true
				observed.CountsKnown = true
				readResults[i] = readResult{capacity: observed, ok: true}
			}
			return nil
		})
	}
	_ = g.Wait()
	for i, scope := range scopeOrder {
		if !readResults[i].ok {
			continue
		}
		capacity := readResults[i].capacity
		for _, accountID := range scopes[scope].accountIDs {
			copy := capacity
			capacities[accountID] = &copy
		}
	}
	return capacities
}

func USTCAccountCapacity(ctx context.Context, cache RPMCache, account *Account) *USTCCapacity {
	if !IsUSTCCapacityAccount(account) {
		return nil
	}
	capacity, ok := USTCAccountCapacitiesBatch(ctx, cache, []Account{*account})[account.ID]
	if !ok {
		return ustcCapacityWithLimits(USTCLimits{}, false)
	}
	return capacity
}
