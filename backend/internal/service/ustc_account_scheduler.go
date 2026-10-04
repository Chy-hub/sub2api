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
	// concurrency prefer the least-used budget.
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
			// High concurrency leaves every usage vector nil, so the budget
			// comparison below ties and the live capacity order is preserved.
			var usage map[int64][]int
			if !highConcurrency {
				now := time.Now()
				usage = make(map[int64][]int, len(ustc))
				for _, account := range ustc {
					usage[account.ID] = ustcQuotaUsageBands(account, now)
				}
			}
			sort.SliceStable(ustc, func(i, j int) bool {
				a, b := capacities[ustc[i].ID], capacities[ustc[j].ID]
				if order := compareUSTCQuotaUsageBands(usage[ustc[i].ID], usage[ustc[j].ID]); order != 0 {
					return order < 0
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
	// ustcQuotaUsageBandWidth 把预算窗口的用量比例量化成档位，同一档内保持原来的
	// 轮询顺序，只有明显更闲的账号才插队。用量快照 30 秒才更新一次，不量化会让
	// 最低用量的账号在整个快照周期独占流量。
	ustcQuotaUsageBandWidth = 0.05
	ustcQuotaUsageMaxBand   = 20
	// ustcQuotaUsageBandRounding 抵消分档除法的浮点误差：0.15/0.05 在 float64 下
	// 是 2.999…，直接截断会把正好压在整档边界上的账号算低一档。
	ustcQuotaUsageBandRounding = 1e-9
	// ustcQuotaUsageStaleBand 比任何可信观测都差，用于快照过期时兜底。
	ustcQuotaUsageStaleBand = ustcQuotaUsageMaxBand + 1

	// ustcQuotaUsageTrustedAge 是排序可以信任用量快照的最大年龄。调度路径每 30 秒
	// 自动补探、失败退避 5 秒，正常流量下快照远新于此；旧到这个界限说明连续探测
	// 失败或长时间空闲，数字不再代表当前约束，不能拿它决定给谁加权。
	ustcQuotaUsageTrustedAge = 2 * time.Minute
)

// ustcPoolHighConcurrency 报告本轮是否按「高并发」分散：空中的 USTC 请求数超过
// 可用账号数时，平均每个账号不止一单在跑，此时按实时容量分散，预算用量让位。
// 用量窗口是每分钟请求数，长流在窗口里只记一次却始终占着并发，因此这里数空中
// 请求而不是数窗口用量。
func ustcPoolHighConcurrency(ctx context.Context, candidates []*Account, capacities map[int64]USTCCapacity) bool {
	accounts, inFlight := 0, ustcPoolDemand(ctx)
	// 容量按物理 Key 共享：重复导入、同 Key 多分组的账号行读到同一份计数，
	// 必须按 Key 指纹去重，否则行数会把账号数和在途数一起放大。
	seen := make(map[string]struct{}, len(candidates))
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
		accounts++
		// InFlight 是未释放的并发租约，已包含尚未发送的预占，不能再加 Pending。
		inFlight += capacities[account.ID].InFlight
	}
	return inFlight > accounts
}

// ustcPoolDemand 是本池还没有拿到租约、仍在等待派发的请求数。一次派发共享同一份
// 容量快照，派发中新建立的预占不会出现在快照里，只看快照会把突发误判成低并发；
// 拿到租约的请求由协调器从等待数里扣掉，改由在途并发统计，两边不重复计。
func ustcPoolDemand(ctx context.Context) int {
	demand, _ := ctx.Value(ustcPoolDemandKey{}).(int)
	return demand
}

func ustcUsageBand(used float64) int {
	// 超出满档的用量按满档处理。这个判断对 NaN 与 +Inf 同样成立，异常快照只会让
	// 账号更靠后，不会反过来变成「最闲」的账号。
	if !(used < float64(ustcQuotaUsageMaxBand)*ustcQuotaUsageBandWidth) {
		return ustcQuotaUsageMaxBand
	}
	// used >= 0，截断即向下取整；加上舍入容差避免整档边界被浮点误差吃低一档。
	return clampUSTCBand(int(used/ustcQuotaUsageBandWidth+ustcQuotaUsageBandRounding), ustcQuotaUsageMaxBand)
}

func clampUSTCBand(band, maximum int) int {
	if band < 0 {
		return 0
	}
	if band > maximum {
		return maximum
	}
	return band
}

// ustcQuotaUsageBands 返回账号各预算窗口的已用档位，从紧张到宽松降序排列。
// 已重置、用量未知、限额非正（上游报 `max_budget: 0` 的「无预算」快照会退化成
// 这种窗口）的条目先被剔除；一条都不剩说明没有可均衡的额度，返回 nil 不降权。
// 只剩过期快照可用时返回兜底档位：旧数字不代表当前约束，宁可退回容量排序，
// 账号本身仍然可选，只是排在所有可信快照之后，直到后台补探刷回来。
// 已知耗尽的窗口由 userInfoQuotaSchedulingFailureReason 拦截，与这里的排序无关。
func ustcQuotaUsageBands(account *Account, now time.Time) []int {
	var bands []int
	for _, window := range userInfoQuotaWindowsForScheduling(account.Extra) {
		// !(Limit > 0) 同时挡掉 0、负数与 NaN 这些算不出比例的窗口。
		if !window.UsedKnown || !(window.Limit > 0) {
			continue
		}
		if window.ResetAt != "" {
			if reset, err := parseUserInfoTime(window.ResetAt); err == nil && !reset.After(now) {
				continue
			}
		}
		bands = append(bands, ustcUsageBand(window.WindowSpend/window.Limit))
	}
	if len(bands) == 0 {
		return nil
	}
	if !ustcQuotaUsageSnapshotTrusted(account.Extra, now) {
		return []int{ustcQuotaUsageStaleBand}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(bands)))
	return bands
}

// ustcQuotaUsageSnapshotTrusted 报告用量快照是否新到可以用于排序。这里比刷新触发
// 的 30 秒有效期宽松：补探在途、偶发失败和短暂空闲都不该让一个健康账号掉队。
func ustcQuotaUsageSnapshotTrusted(extra map[string]any, now time.Time) bool {
	return userInfoQuotaExtraAgeWithin(extra, now, ustcQuotaUsageTrustedAge)
}

// compareUSTCQuotaUsageBands 按「最紧张的窗口优先」比较档位向量：先比最高档，
// 相同再比次高档，缺失的档位按 0 补齐。
func compareUSTCQuotaUsageBands(a, b []int) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x - y
		}
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
