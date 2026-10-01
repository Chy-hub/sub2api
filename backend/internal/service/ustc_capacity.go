package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strings"
	"time"
)

// USTCCapacity observes one physical upstream Key. Null limits mean unlimited
// only for known metadata; state "unknown" never implies unlimited capacity.
type USTCCapacity struct {
	RPMLimit      *int      `json:"rpm_limit"`
	ParallelLimit *int      `json:"parallel_limit"`
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
	if !isDefaultUSTCAccount(account) {
		return ""
	}
	key := strings.TrimSpace(account.GetCredential("api_key"))
	if key == "" {
		return ""
	}
	fingerprint := sha256.Sum256([]byte(key))
	return hex.EncodeToString(fingerprint[:])
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

func USTCAccountCapacity(ctx context.Context, cache RPMCache, account *Account) *USTCCapacity {
	if !isDefaultUSTCAccount(account) {
		return nil
	}
	limits, known := USTCAccountLimits(account)
	capacity := USTCCapacity{State: "unknown"}
	if !known {
		return &capacity
	}
	if limits.RPM > 0 {
		capacity.RPMLimit = &limits.RPM
	}
	if limits.Parallel > 0 {
		capacity.ParallelLimit = &limits.Parallel
	}
	if store, ok := cache.(USTCCapacityCache); ok {
		if observed, err := store.USTCRead(ctx, USTCKeyScope(account), limits); err == nil {
			capacity = observed
		}
	}
	return &capacity
}
