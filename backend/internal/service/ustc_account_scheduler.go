package service

import (
	"context"
	"sort"
	"time"
)

type skipDefaultUSTCBalancingKey struct{}
type ustcQuotaAdmissionKey struct{}
type ustcQuotaBackgroundAdmissionKey struct{}

func defaultUSTCAccountBalancingEnabled(accounts []Account) bool {
	for i := range accounts {
		if isDefaultUSTCAccount(&accounts[i]) {
			return true
		}
	}
	return false
}
func isDefaultUSTCAccount(account *Account) bool {
	return IsUSTCCapacityAccount(account)
}
func (s *OpenAIGatewayService) refreshUSTCQuotaDuringCandidateCheck(ctx context.Context, account *Account) *Account {
	if deferred, _ := ctx.Value(deferUSTCQuotaEligibilityKey{}).(bool); deferred {
		return account
	}
	if admission, _ := ctx.Value(ustcQuotaAdmissionKey{}).(bool); admission && isDefaultUSTCAccount(account) {
		if refresher, ok := s.ustcQuotaRefresher.(USTCQuotaAdmissionRefresher); ok {
			refreshed, ready := refresher.QuotaForAdmission(ctx, account)
			if !ready {
				return nil
			}
			return refreshed
		}
	}
	return s.refreshUSTCQuotaForScheduling(ctx, account)
}

func (s *OpenAIGatewayService) selectBalancedDefaultUSTCAccount(
	ctx context.Context, groupID *int64, accounts []Account, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requireCompact bool, capability OpenAIEndpointCapability, preferLowRate bool,
	requiredTransport OpenAIUpstreamTransport,
) (*AccountSelectionResult, error) {
	stats := openAISelectionFilterStats{pool: len(accounts)}
	var retryAt time.Time
	mutable := false
	recoverAt := func(at time.Time, canChange bool) {
		if at.IsZero() {
			at = time.Now().Add(time.Second)
		}
		if retryAt.IsZero() || at.Before(retryAt) {
			retryAt = at
		}
		mutable = mutable || canChange
	}
	initialCtx := context.WithValue(ctx, deferUSTCQuotaEligibilityKey{}, true)
	var candidates []*Account
	capacities := make(map[int64]USTCCapacity)
	cache, configured := s.rpmCache.(USTCCapacityCache)
	for i := range accounts {
		account := &accounts[i]
		if _, excluded := excludedIDs[account.ID]; excluded {
			stats.exclude("excluded")
			continue
		}
		if !s.isOpenAIAccountTransportCompatible(account, requiredTransport) {
			stats.exclude("transport_mismatch")
			continue
		}
		if reason := openAICompatibleAccountEligibilityFailureReason(initialCtx, account, PlatformOpenAI, requestedModel, false, capability); reason != "" {
			stats.exclude(reason)
			if isDefaultUSTCAccount(account) && account.IsActive() && account.Schedulable && account.IsModelSupported(requestedModel) && account.SupportsOpenAIEndpointCapability(capability) {
				if reason == "not_schedulable" {
					recoverAt(ustcAccountCoolingUntil(account, time.Now()), false)
				}
				if reason == "model_rate_limited" {
					recoverAt(time.Now().Add(time.Second), true)
				}
				if reason == "upstream_userinfo_window_exhausted" {
					recoverAt(ustcQuotaNextCheck(account, time.Now()), true)
				}
			}
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel) {
			stats.exclude("runtime_blocked")
			continue
		}
		if isDefaultUSTCAccount(account) {
			account = s.ustcAccountForAdmission(ctx, account)
			if account == nil {
				stats.exclude("limits_unknown")
				recoverAt(time.Now().Add(time.Second), true)
				continue
			}
			if reason := userInfoQuotaSchedulingFailureReason(account, time.Now()); reason != "" {
				stats.exclude(reason)
				if reason == "upstream_userinfo_window_exhausted" {
					recoverAt(ustcQuotaNextCheck(account, time.Now()), true)
				}
				continue
			}
			limits, _ := USTCAccountLimits(account)
			if !configured {
				stats.exclude("capacity_unavailable")
				recoverAt(time.Now().Add(time.Second), true)
				continue
			}
			capacity, err := readUSTCCapacity(ctx, cache, USTCKeyScope(account), limits)
			if err != nil {
				stats.exclude("capacity_unavailable")
				recoverAt(time.Now().Add(time.Second), true)
				continue
			}
			capacities[account.ID] = capacity
			rpmLimit := limits.RPM
			if capacity.RPMLimit != nil {
				rpmLimit = *capacity.RPMLimit
			}
			if capacity.State == "sync_wait" || capacity.State == "unknown" || (capacity.State == "verify_one" && capacity.Available <= 0) || (rpmLimit > 0 && capacity.Used+capacity.Pending >= rpmLimit) || (limits.Parallel > 0 && capacity.InFlight >= limits.Parallel) {
				stats.exclude("ustc_capacity")
				recoverAt(capacity.ResetAt, ustcCapacityCanRecoverEarly(capacity))
				continue
			}
		}
		candidates = append(candidates, account)
	}
	rateOrder := openAILegacyUpstreamRateOrder{}
	if preferLowRate {
		rateOrder = newOpenAILegacyUpstreamRateOrder(candidates, time.Now(), s.openAIOAuthSchedulingRateMultiplier(ctx))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if rateOrder.enabled {
			if order := rateOrder.compare(a, b); order != 0 {
				return order < 0
			}
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return s.isBetterAccount(a, b)
	})
	// Preserve non-USTC ordering. Balance USTC only within the same priority/cost:
	// under high concurrency keep the original live-capacity order, under low
	// concurrency prefer the key with the most remaining quota.
	highConcurrency := ustcPoolHighConcurrency(ctx, candidates, capacities)
	state := s.defaultUSTCPoolState()
	state.mu.Lock()
	offset := state.cursor
	state.cursor++
	state.mu.Unlock()
	for start := 0; start < len(candidates); {
		end := start + 1
		for end < len(candidates) && candidates[end].Priority == candidates[start].Priority && rateOrder.compare(candidates[start], candidates[end]) == 0 {
			end++
		}
		var positions []int
		var ustc []*Account
		for i := start; i < end; i++ {
			if isDefaultUSTCAccount(candidates[i]) {
				positions = append(positions, i)
				ustc = append(ustc, candidates[i])
			}
		}
		if len(ustc) > 0 {
			sort.SliceStable(ustc, func(i, j int) bool { return ustc[i].ID < ustc[j].ID })
			shift := int(offset % uint64(len(ustc)))
			ustc = append(ustc[shift:], ustc[:shift]...)
			// High concurrency leaves every remaining slice nil, so the budget
			// comparison below ties and the live capacity order is preserved.
			var remaining map[int64][]ustcQuotaRemaining
			var trusted map[int64]bool
			var windowOrder []string
			if !highConcurrency {
				now := time.Now()
				remaining = make(map[int64][]ustcQuotaRemaining, len(ustc))
				trusted = make(map[int64]bool, len(ustc))
				allWindows := make([][]ustcQuotaRemaining, 0, len(ustc))
				for _, account := range ustc {
					windows, fresh := ustcQuotaRemainingWindows(account, now)
					remaining[account.ID] = windows
					trusted[account.ID] = fresh
					allWindows = append(allWindows, windows)
				}
				windowOrder = ustcQuotaWindowOrder(allWindows...)
			}
			sort.SliceStable(ustc, func(i, j int) bool {
				a, b := capacities[ustc[i].ID], capacities[ustc[j].ID]
				left, right := remaining[ustc[i].ID], remaining[ustc[j].ID]
				// A key without usable windows carries no ordering signal; let the
				// live capacity/rotation tie-break decide instead of treating missing
				// data as an empty budget.
				if len(left) > 0 && len(right) > 0 {
					if trusted[ustc[i].ID] != trusted[ustc[j].ID] {
						return trusted[ustc[i].ID]
					}
					if order := compareUSTCQuotaRemaining(left, right, windowOrder); order != 0 {
						return order < 0
					}
				}
				x, y := ustcCapacityRatio(a), ustcCapacityRatio(b)
				if x != y {
					return x < y
				}
				return a.InFlight < b.InFlight
			})
			for i, position := range positions {
				candidates[position] = ustc[i]
			}
		}
		start = end
	}
	stickyID := int64(0)
	if sessionHash != "" && s.cache != nil {
		stickyID, _ = s.getStickySessionAccountID(ctx, groupID, sessionHash)
	}
	for i, account := range candidates {
		if account.ID == stickyID && !isDefaultUSTCAccount(account) {
			copy(candidates[1:i+1], candidates[:i])
			candidates[0] = account
			break
		}
	}
	var waiting *Account
	stickySpillover := false
	compactBlocked := false
	for _, account := range candidates {
		managed := isDefaultUSTCAccount(account)
		candidateCtx := ctx
		if managed {
			candidateCtx = context.WithValue(ctx, ustcQuotaAdmissionKey{}, true)
			candidateCtx = context.WithValue(candidateCtx, ustcQuotaBackgroundAdmissionKey{}, true)
		}
		fresh := s.resolveFreshSchedulableOpenAIAccount(candidateCtx, account, PlatformOpenAI, requestedModel, false, capability)
		if fresh != nil {
			fresh = s.recheckSelectedOpenAIAccountFromDB(candidateCtx, fresh, groupID, PlatformOpenAI, requestedModel, requireCompact, capability)
		}
		if fresh == nil {
			stats.exclude("candidate_changed")
			if managed {
				recoverAt(time.Now().Add(time.Second), true)
			}
			continue
		}
		if !s.isOpenAIAccountTransportCompatible(fresh, requiredTransport) {
			stats.exclude("transport_mismatch")
			continue
		}
		if requireCompact && openAICompactSupportTier(fresh) == 0 {
			compactBlocked = true
			continue
		}
		if s.needsUpstreamChannelRestrictionCheck(ctx, groupID) && s.isUpstreamModelRestrictedByChannel(ctx, derefGroupID(groupID), fresh, requestedModel, requireCompact) {
			continue
		}
		maxConcurrency := fresh.Concurrency
		if managed {
			if limits, known := USTCAccountLimits(fresh); known {
				maxConcurrency = limits.Parallel
				fresh = copyAccountForUserInfoQuotaRefresh(fresh)
				fresh.Concurrency = maxConcurrency
			}
		}
		acquired, err := s.tryAcquireAccountSlot(ctx, fresh.ID, maxConcurrency)
		if err != nil || acquired == nil || !acquired.Acquired {
			if managed {
				recoverAt(time.Now().Add(time.Second), true)
			} else {
				if fresh.ID == stickyID && s.concurrencyService != nil {
					cfg := s.schedulingConfig()
					waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, fresh.ID)
					if waitingCount < cfg.StickySessionMaxWaiting {
						selection, err := s.newSelectionResult(ctx, fresh, false, nil, &AccountWaitPlan{AccountID: fresh.ID, MaxConcurrency: fresh.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting})
						return markStickySessionHit(selection, true), err
					}
					stickySpillover = true
				}
				if waiting == nil {
					waiting = fresh
				}
			}
			continue
		}
		var reservation *ustcAdmission
		if managed {
			var capacity USTCCapacity
			reservation, capacity, err = s.reserveUSTC(ctx, fresh)
			if err != nil || reservation == nil {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				recoverAt(capacity.ResetAt, ustcCapacityCanRecoverEarly(capacity) || err != nil)
				continue
			}
		}
		selection, err := s.newAcquiredSelectionResult(ctx, fresh, acquired.ReleaseFunc)
		if err != nil {
			if reservation != nil {
				reservation.release()
			}
			return nil, err
		}
		if !s.isOpenAIAccountTransportCompatible(selection.Account, requiredTransport) {
			if reservation != nil {
				reservation.release()
			}
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			stats.exclude("transport_mismatch")
			continue
		}
		selection.ustcAdmission = reservation
		selection.ReleaseFunc = selection.ReleaseWithUSTCAdmission(selection.ReleaseFunc)
		if sessionHash != "" && !stickySpillover && !gatewayProfitControlGateActive(ctx) {
			_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
		}
		return markStickySessionHit(selection, !managed && fresh.ID == stickyID), nil
	}
	if waiting != nil {
		cfg := s.schedulingConfig()
		plan := &AccountWaitPlan{AccountID: waiting.ID, MaxConcurrency: waiting.Concurrency, Timeout: cfg.FallbackWaitTimeout, MaxWaiting: cfg.FallbackMaxWaiting}
		stickyHit := waiting.ID == stickyID
		if stickyHit && s.concurrencyService != nil {
			waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, waiting.ID)
			if waitingCount < cfg.StickySessionMaxWaiting {
				plan.Timeout = cfg.StickySessionWaitTimeout
				plan.MaxWaiting = cfg.StickySessionMaxWaiting
			}
		}
		selection, err := s.newSelectionResult(ctx, waiting, false, nil, plan)
		return markStickySessionHit(selection, stickyHit), err
	}
	err := noAvailableOpenAISelectionError(requestedModel, compactBlocked, stats.summary(""))
	if !retryAt.IsZero() && !compactBlocked {
		return nil, &ustcPoolCapacityError{cause: err, retryAfter: max(time.Millisecond, time.Until(retryAt)), mutable: mutable}
	}
	return nil, err
}
func ustcCapacityRatio(capacity USTCCapacity) float64 {
	if capacity.RPMLimit != nil && *capacity.RPMLimit > 0 {
		return float64(capacity.Used+capacity.Pending) / float64(*capacity.RPMLimit)
	}
	return 0
}

const (
	// ustcQuotaRemainingEpsilon absorbs float parsing noise: two observations of
	// the same shared window can differ in the last bits without representing a
	// real imbalance.
	ustcQuotaRemainingEpsilon = 1e-9
	// ustcQuotaRemainingTrustedAge only guards against a probe that has been
	// failing for a long time. A shorter limit would disable ordering exactly
	// when traffic is sparse enough that every snapshot looks old; windows that
	// already reset are dropped separately, so old numbers cannot survive a
	// window boundary.
	ustcQuotaRemainingTrustedAge = 30 * time.Minute
)

// ustcQuotaRemaining is one usable budget window's remaining quota. Windows are
// matched by duration, never by position: a key whose probe omitted a window
// (or whose upstream stopped reporting usage for it) must not look richer than
// a key that reports it.
type ustcQuotaRemaining struct {
	key       string
	limit     float64
	remaining float64
}

// ustcQuotaRemainingWindows returns remaining quota (limit - window spend) for
// every usable window. Exact remaining, not a quantized usage ratio, keeps the
// comparison smooth: the key with the most quota left wins only until another
// key catches up within roughly one refresh cycle, instead of monopolizing
// traffic until it crosses the next 5% band.
func ustcQuotaRemainingWindows(account *Account, now time.Time) ([]ustcQuotaRemaining, bool) {
	windows := userInfoQuotaWindowsForScheduling(account.Extra)
	remaining := make([]ustcQuotaRemaining, 0, len(windows))
	seen := make(map[string]struct{}, len(windows))
	for _, window := range windows {
		// /user/info budgets are shared by every key of the same upstream user:
		// their remaining quota is identical across accounts and cannot rank
		// them. Exact comparison would otherwise let probe-timing noise on the
		// shared window outrank the per-key windows. Legacy snapshots carry no
		// scope and are treated as key-scope budget.
		if window.Scope == UserInfoWindowScopeUser {
			continue
		}
		// !(Limit > 0) also rejects 0, negative and NaN windows, which cannot
		// express a comparable budget.
		if !window.UsedKnown || !(window.Limit > 0) {
			continue
		}
		if window.ResetAt != "" {
			if reset, err := parseUserInfoTime(window.ResetAt); err == nil && !reset.After(now) {
				continue
			}
		}
		// Multiple windows with the same duration describe the same budget;
		// compare them once.
		if _, duplicate := seen[window.Duration]; duplicate {
			continue
		}
		seen[window.Duration] = struct{}{}
		remaining = append(remaining, ustcQuotaRemaining{
			key:       window.Duration,
			limit:     window.Limit,
			remaining: window.Limit - window.WindowSpend,
		})
	}
	if len(remaining) == 0 {
		return nil, false
	}
	sort.SliceStable(remaining, func(i, j int) bool { return remaining[i].key < remaining[j].key })
	return remaining, userInfoQuotaExtraAgeWithin(account.Extra, now, ustcQuotaRemainingTrustedAge)
}

// ustcQuotaWindowOrder ranks window durations by how tight the pool's remaining
// fraction is: the window closest to exhaustion decides first, and windows that
// still have plenty of headroom only break ties. Ordering by duration instead
// would let an almost-idle 3h window hide a nearly exhausted 12h/24h window.
func ustcQuotaWindowOrder(accounts ...[]ustcQuotaRemaining) []string {
	tightest := make(map[string]float64)
	for _, windows := range accounts {
		for _, window := range windows {
			fraction := window.remaining / window.limit
			if current, ok := tightest[window.key]; !ok || fraction < current {
				tightest[window.key] = fraction
			}
		}
	}
	order := make([]string, 0, len(tightest))
	for key := range tightest {
		order = append(order, key)
	}
	sort.Slice(order, func(i, j int) bool {
		if tightest[order[i]] != tightest[order[j]] {
			return tightest[order[i]] < tightest[order[j]]
		}
		return order[i] < order[j]
	})
	return order
}

// compareUSTCQuotaRemaining compares absolute remaining quota on the windows
// both keys report, in the pool's tightest-first order. Windows present on only
// one side are skipped: missing data is not an empty budget. A zero result
// means nothing comparable separates the keys, so the caller falls back to live
// capacity and rotation.
func compareUSTCQuotaRemaining(a, b []ustcQuotaRemaining, order []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	left := make(map[string]float64, len(a))
	for _, window := range a {
		left[window.key] = window.remaining
	}
	right := make(map[string]float64, len(b))
	for _, window := range b {
		right[window.key] = window.remaining
	}
	for _, key := range order {
		l, lok := left[key]
		r, rok := right[key]
		if !lok || !rok {
			continue
		}
		if l > r+ustcQuotaRemainingEpsilon {
			return -1
		}
		if l < r-ustcQuotaRemainingEpsilon {
			return 1
		}
	}
	return 0
}

// ustcPoolHighConcurrency reports whether this pass is under real capacity
// pressure: more requests are waiting for a slot than the remaining admission
// capacity of the candidate keys. Only then does live capacity outrank
// remaining budget.
//
// Counting in-flight requests against the number of keys would call a handful
// of concurrent streams on a 20-parallel key "high concurrency" and silently
// disable budget balancing for essentially all real traffic. Each key's
// Available already subtracts its own in-flight leases and pending RPM
// reservations, so summing it gives the number of requests the pool can still
// admit right now.
func ustcPoolHighConcurrency(ctx context.Context, candidates []*Account, capacities map[int64]USTCCapacity) bool {
	demand := ustcPoolDemand(ctx)
	if demand <= 0 {
		// Direct selections (previous_response_id, single-account admission) carry
		// no queued demand; there is no pressure to trade balancing against.
		return false
	}
	// Capacity is shared by physical key: duplicate imports and one key in several
	// groups read the same counters, so counting every row would overstate the
	// remaining capacity and hide a saturated pool.
	seen := make(map[string]struct{}, len(candidates))
	available := 0
	for _, account := range candidates {
		if !isDefaultUSTCAccount(account) {
			continue
		}
		if scope := USTCKeyScope(account); scope != "" {
			if _, counted := seen[scope]; counted {
				continue
			}
			seen[scope] = struct{}{}
		}
		available += max(0, capacities[account.ID].Available)
	}
	return demand > available
}

// ustcPoolDemand 是本池还没有拿到租约、仍在等待派发的请求数。一次派发共享同一份
// 容量快照，派发中新建立的预占不会出现在快照里，只看快照会把突发误判成低并发；
// 拿到租约的请求由协调器从等待数里扣掉，改由在途并发统计，两边不重复计。
func ustcPoolDemand(ctx context.Context) int {
	demand, _ := ctx.Value(ustcPoolDemandKey{}).(int)
	return demand
}

func ustcCapacityCanRecoverEarly(capacity USTCCapacity) bool {
	if capacity.State == "sync_wait" {
		return false
	}
	if capacity.RPMLimit != nil && *capacity.RPMLimit > 0 && capacity.Used >= *capacity.RPMLimit {
		// Closing an already sent stream releases concurrency, never its RPM.
		return false
	}
	return capacity.Pending > 0 || capacity.InFlight > 0 || capacity.State == "unknown"
}
