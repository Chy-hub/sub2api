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
	// Preserve non-USTC ordering. Balance USTC only within the same priority/cost.
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
			sort.SliceStable(ustc, func(i, j int) bool {
				a, b := capacities[ustc[i].ID], capacities[ustc[j].ID]
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
