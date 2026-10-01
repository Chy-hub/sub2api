package service

import (
	"context"
	"log/slog"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Native WebSocket admission owns turn-level accounting separately from the
// HTTP handlers that commit these RPM reservations.
type skipDefaultUSTCBalancingKey struct{}

// Selected USTC accounts may be rechecked while a slot is held. In that phase
// only the immediate admission reader may refresh quota observations.
type ustcQuotaAdmissionKey struct{}

// A reservation is refunded only when admission fails before forwarding.
type openAIAccountRPMReservation struct {
	mu        sync.Mutex
	committed bool
	released  bool
	cancel    func()
	onCommit  func()
}

func (r *openAIAccountRPMReservation) commit() {
	r.mu.Lock()
	if r.released || r.committed {
		r.mu.Unlock()
		return
	}
	r.committed = true
	onCommit := r.onCommit
	r.mu.Unlock()
	if onCommit != nil {
		onCommit()
	}
}

func (r *openAIAccountRPMReservation) release() {
	r.mu.Lock()
	if r.released || r.committed {
		r.mu.Unlock()
		return
	}
	r.released = true
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RPMReserved distinguishes admission-time accounting from legacy success accounting.
func (r *AccountSelectionResult) RPMReserved() bool {
	return r != nil && r.rpmReservation != nil
}

func (r *AccountSelectionResult) ReleaseRPMReservation() {
	if r != nil && r.rpmReservation != nil {
		r.rpmReservation.release()
	}
}

func (r *AccountSelectionResult) CommitRPMReservation() {
	if r != nil && r.rpmReservation != nil {
		r.rpmReservation.commit()
	}
}

// This personal routing policy applies only to the known USTC upstream.
func defaultUSTCAccountBalancingEnabled(accounts []Account) bool {
	for i := range accounts {
		if isDefaultUSTCAccount(&accounts[i]) {
			return true
		}
	}
	return false
}

func isDefaultUSTCAccount(account *Account) bool {
	return account != nil && account.IsOpenAI() && account.Type == AccountTypeAPIKey && account.SupportsUserInfoQuota()
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

func (s *OpenAIGatewayService) reserveDefaultAccountRPM(ctx context.Context, account *Account, sticky bool) (*openAIAccountRPMReservation, bool) {
	if s.rpmCache == nil {
		return nil, true
	}
	limit := account.GetBaseRPM()
	if sticky && limit > 0 {
		if account.GetRPMStrategy() == "sticky_exempt" {
			limit = 0
		} else {
			limit += account.GetRPMStickyBuffer()
		}
	}
	if cache, ok := s.rpmCache.(RPMReservationCache); ok {
		allowed, cancel, err := cache.ReserveRPM(ctx, account.ID, limit)
		if err != nil {
			slog.Warn("openai_default_rpm_reservation_failed", "account_id", account.ID, "error", err)
			return nil, true // retain the existing Redis fail-open policy
		}
		if !allowed {
			return nil, false
		}
		return &openAIAccountRPMReservation{cancel: cancel}, true
	}
	// Compatibility for implementations without reservations (e.g. older plugins).
	count, err := s.rpmCache.GetRPM(ctx, account.ID)
	if err != nil {
		return nil, true
	}
	if limit > 0 && count >= limit {
		return nil, false
	}
	// Older caches cannot atomically reserve or refund. Delay their accounting
	// until forwarding admission, so discarded selections consume no RPM.
	return &openAIAccountRPMReservation{onCommit: func() {
		commitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := s.rpmCache.IncrementRPM(commitCtx, account.ID); err != nil {
			slog.Warn("openai_default_rpm_commit_failed", "account_id", account.ID, "error", err)
		}
	}}, true
}

// PrepareDefaultAccountRPM also covers slots acquired after waiting: the RPM
// window is checked at admission rather than reserved throughout a long queue.
func (s *OpenAIGatewayService) PrepareDefaultAccountRPM(ctx context.Context, selection *AccountSelectionResult) bool {
	if selection == nil || selection.Account == nil || (!selection.defaultRPMManaged && !isDefaultUSTCAccount(selection.Account)) {
		return true
	}
	if refresher, ok := s.ustcQuotaRefresher.(USTCQuotaAdmissionRefresher); ok && isDefaultUSTCAccount(selection.Account) {
		account, ready := refresher.QuotaForAdmission(ctx, selection.Account)
		if account != nil {
			selection.Account = account
		}
		if !ready {
			// Release this slot while the shared refresh proceeds in background.
			return false
		}
	} else {
		selection.Account = s.refreshUSTCQuotaForScheduling(ctx, selection.Account)
	}
	if userInfoQuotaSchedulingFailureReason(selection.Account, time.Now()) != "" {
		return false
	}
	if !selection.defaultRPMManaged {
		return true
	}
	if selection.RPMReserved() {
		return true
	}
	reservation, allowed := s.reserveDefaultAccountRPM(ctx, selection.Account, selection.rpmSticky)
	selection.rpmReservation = reservation
	return allowed
}

func (s *OpenAIGatewayService) selectBalancedDefaultUSTCAccount(
	ctx context.Context, groupID *int64, accounts []Account, sessionHash, requestedModel string,
	excludedIDs map[int64]struct{}, requireCompact bool, capability OpenAIEndpointCapability, preferLowRate bool,
) (*AccountSelectionResult, error) {
	stickyID := int64(0)
	if sessionHash != "" && s.cache != nil {
		stickyID, _ = s.getStickySessionAccountID(ctx, groupID, sessionHash)
	}
	stats := openAISelectionFilterStats{pool: len(accounts)}
	initialCtx := ctx
	if s.ustcQuotaRefresher != nil {
		// Refresh only the selected candidates, including stale exhausted ones.
		initialCtx = context.WithValue(ctx, deferUSTCQuotaEligibilityKey{}, true)
	}
	var candidates []*Account
	var ids []int64
	for i := range accounts {
		account := &accounts[i]
		if _, excluded := excludedIDs[account.ID]; excluded {
			stats.exclude("excluded")
			continue
		}
		if reason := openAICompatibleAccountEligibilityFailureReason(initialCtx, account, PlatformOpenAI, requestedModel, false, capability); reason != "" {
			stats.exclude(reason)
			continue
		}
		if s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel) {
			stats.exclude("runtime_blocked")
			continue
		}
		candidates = append(candidates, account)
		if isDefaultUSTCAccount(account) {
			ids = append(ids, account.ID)
		}
	}
	counts := map[int64]int{}
	if s.rpmCache != nil && len(ids) > 0 {
		if batch, err := s.rpmCache.GetRPMBatch(ctx, ids); err == nil {
			counts = batch
		}
	}
	loads := map[int64]*AccountLoadInfo{}
	if s.concurrencyService != nil && s.schedulingConfig().LoadBatchEnabled {
		if batch, err := s.concurrencyService.GetAccountsLoadBatch(ctx, buildOpenAIAccountLoadRequest(candidates)); err == nil {
			loads = batch
		}
	}
	// Random ties are deliberate: LastUsedAt and cached load are shared snapshots,
	// so deterministic ties make concurrent requests flock to the same account.
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
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
		loadA, loadB := defaultUSTCLoadRate(loads, a.ID), defaultUSTCLoadRate(loads, b.ID)
		if loadA != loadB {
			return loadA < loadB
		}
		return s.isBetterAccount(a, b)
	})
	// Keep other upstreams in their legacy positions. Within each cost/priority
	// cohort, only USTC accounts trade places according to their current RPM.
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
		rand.Shuffle(len(ustc), func(i, j int) { ustc[i], ustc[j] = ustc[j], ustc[i] })
		sort.SliceStable(ustc, func(i, j int) bool {
			a, b := ustc[i], ustc[j]
			if counts[a.ID] != counts[b.ID] {
				return counts[a.ID] < counts[b.ID]
			}
			return defaultUSTCLoadRate(loads, a.ID) < defaultUSTCLoadRate(loads, b.ID)
		})
		for i, position := range positions {
			candidates[position] = ustc[i]
		}
		start = end
	}
	// A non-USTC sticky binding retains its original preference.
	for i, account := range candidates {
		if account.ID == stickyID && !isDefaultUSTCAccount(account) {
			copy(candidates[1:i+1], candidates[:i])
			candidates[0] = account
			break
		}
	}
	var waiting *Account
	var waitingSticky bool
	stickySpillover := false
	compactBlocked := false
	// Green accounts always precede the sticky buffer, irrespective of priority.
	for _, stickyPass := range []bool{false, true} {
		for _, account := range candidates {
			managed := isDefaultUSTCAccount(account)
			if stickyPass && (!managed || stickyID <= 0 || account.ID != stickyID) {
				continue
			}
			current := counts[account.ID]
			if managed && account.GetBaseRPM() > 0 && (account.CheckRPMSchedulability(current) == WindowCostNotSchedulable ||
				(!stickyPass && current >= account.GetBaseRPM())) {
				stats.exclude("rpm_exceeded")
				continue
			}
			fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, account, PlatformOpenAI, requestedModel, false, capability)
			if fresh != nil {
				fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, groupID, PlatformOpenAI, requestedModel, requireCompact, capability)
			}
			if fresh == nil {
				continue
			}
			if requireCompact && openAICompactSupportTier(fresh) == 0 {
				compactBlocked = true
				continue
			}
			if s.needsUpstreamChannelRestrictionCheck(ctx, groupID) && s.isUpstreamModelRestrictedByChannel(ctx, derefGroupID(groupID), fresh, requestedModel, requireCompact) {
				continue
			}
			acquired, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency)
			if err != nil || acquired == nil || !acquired.Acquired {
				if fresh.ID == stickyID && !isDefaultUSTCAccount(fresh) && s.concurrencyService != nil {
					cfg := s.schedulingConfig()
					waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, fresh.ID)
					if waitingCount < cfg.StickySessionMaxWaiting {
						selection, err := s.newSelectionResult(ctx, fresh, false, nil, &AccountWaitPlan{
							AccountID: fresh.ID, MaxConcurrency: fresh.Concurrency,
							Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting,
						})
						return markStickySessionHit(selection, true), err
					}
					stickySpillover = true
				}
				if waiting == nil {
					waiting, waitingSticky = fresh, stickyPass
				}
				continue
			}
			var reservation *openAIAccountRPMReservation
			allowed := true
			managed = isDefaultUSTCAccount(fresh)
			if managed {
				reservation, allowed = s.reserveDefaultAccountRPM(ctx, fresh, stickyPass)
			}
			if !allowed {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				continue
			}
			selection, err := s.newAcquiredSelectionResult(ctx, fresh, acquired.ReleaseFunc)
			if err != nil {
				if reservation != nil {
					reservation.release()
				}
				return nil, err
			}
			selection.defaultRPMManaged, selection.rpmSticky, selection.rpmReservation = managed, stickyPass, reservation
			releaseSlot := selection.ReleaseFunc
			selection.ReleaseFunc = func() {
				selection.ReleaseRPMReservation()
				if releaseSlot != nil {
					releaseSlot()
				}
			}
			if sessionHash != "" && !stickySpillover && !gatewayProfitControlGateActive(ctx) {
				_ = s.setStickySessionAccountID(ctx, groupID, sessionHash, fresh.ID, openaiStickySessionTTL)
			}
			return markStickySessionHit(selection, stickyPass || (!managed && fresh.ID == stickyID)), nil
		}
	}
	if waiting != nil {
		cfg := s.schedulingConfig()
		plan := &AccountWaitPlan{
			AccountID: waiting.ID, MaxConcurrency: waiting.Concurrency,
			Timeout: cfg.FallbackWaitTimeout, MaxWaiting: cfg.FallbackMaxWaiting,
		}
		stickyHit := stickyID > 0 && waiting.ID == stickyID
		if stickyHit && s.concurrencyService != nil {
			waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, waiting.ID)
			if waitingCount < cfg.StickySessionMaxWaiting {
				plan.Timeout, plan.MaxWaiting = cfg.StickySessionWaitTimeout, cfg.StickySessionMaxWaiting
			}
		}
		selection, err := s.newSelectionResult(ctx, waiting, false, nil, plan)
		if err == nil {
			selection.defaultRPMManaged, selection.rpmSticky = isDefaultUSTCAccount(waiting), waitingSticky
		}
		return markStickySessionHit(selection, stickyHit), err
	}
	return nil, noAvailableOpenAISelectionError(requestedModel, compactBlocked, stats.summary(""))
}

func defaultUSTCLoadRate(loads map[int64]*AccountLoadInfo, id int64) int {
	if load := loads[id]; load != nil {
		return load.LoadRate
	}
	return 0
}
