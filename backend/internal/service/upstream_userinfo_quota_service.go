package service

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// 上游 /key/info 额度探测服务（LiteLLM 网关，如 api.llm.ustc.edu.cn）。
//
// GET {base}/key/info + Bearer api_key → 直接返回当前 key 的预算信息
// （max_budget / spend / budget_limits / budget_limits_usage）。短窗口的
// 窗口内用量从 budget_limits_usage[duration].current_spend 直读，无需基线。
// 结果落 account.Extra 快照，供账号列表用量窗口展示余额。

const (
	userInfoQuotaUpstreamTimeout = 15 * time.Second
	userInfoQuotaMaxBodyBytes    = 256 * 1024

	userInfoQuotaSchedulingFreshness = 30 * time.Second
	userInfoQuotaSchedulingBackoff   = 5 * time.Second
	userInfoQuotaSchedulingCacheIdle = 5 * time.Minute
	userInfoQuotaSchedulingCacheMax  = 4096
	userInfoQuotaSchedulingMaxProbes = 16
)

// UpstreamUserInfoQuotaService 探测已知 LiteLLM 网关的预算额度。
type UpstreamUserInfoQuotaService struct {
	accountRepo  AccountRepository
	proxyRepo    ProxyRepository
	httpUpstream HTTPUpstream
	cfg          *config.Config
	onRefresh    func()
	flight       singleflight.Group

	refreshFlight     singleflight.Group
	refreshMu         sync.Mutex
	refreshCache      map[int64]*userInfoQuotaRefreshCacheEntry
	refreshLRU        *list.List
	refreshPending    map[int64]bool
	refreshProbeOnce  sync.Once
	refreshProbeSlots chan struct{}
}

var _ USTCQuotaAdmissionRefresher = (*UpstreamUserInfoQuotaService)(nil)

type userInfoQuotaRefreshCacheEntry struct {
	snapshot     map[string]any
	successAt    time.Time
	lastAttempt  time.Time
	lastError    error
	lastAccessAt time.Time
	lruElement   *list.Element
}

type userInfoQuotaRefreshFlightResult struct {
	snapshot map[string]any
	err      error
}

// NewUpstreamUserInfoQuotaService 构造 /key/info 额度探测服务。
func NewUpstreamUserInfoQuotaService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
) *UpstreamUserInfoQuotaService {
	return &UpstreamUserInfoQuotaService{
		accountRepo:  accountRepo,
		proxyRepo:    proxyRepo,
		httpUpstream: httpUpstream,
		cfg:          cfg,
	}
}

// QueryQuota 探测指定账号的预算额度并落 Extra 快照。
func (s *UpstreamUserInfoQuotaService) QueryQuota(ctx context.Context, accountID int64) (*UserInfoQuotaResult, error) {
	account, err := s.loadAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.QueryQuotaForAccount(ctx, account)
}

// QueryQuotaForAccount 探测已加载账号（避免二次 GetByID）。
func (s *UpstreamUserInfoQuotaService) QueryQuotaForAccount(ctx context.Context, account *Account) (*UserInfoQuotaResult, error) {
	if s == nil || s.accountRepo == nil || s.httpUpstream == nil {
		return nil, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_NOT_CONFIGURED", "upstream userinfo quota service is not configured")
	}
	if err := validateUserInfoQuotaAccount(account); err != nil {
		return nil, err
	}
	key := "userinfo_quota:" + strconv.FormatInt(account.ID, 10)
	resultCh := s.flight.DoChan(key, func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.Background(), 2*userInfoQuotaUpstreamTimeout+5*time.Second)
		defer cancel()
		return s.queryQuotaForAccount(probeCtx, account)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case flightResult := <-resultCh:
		if flightResult.Err != nil {
			return nil, flightResult.Err
		}
		result, ok := flightResult.Val.(*UserInfoQuotaResult)
		if !ok || result == nil {
			return nil, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_RESULT_INVALID", "invalid upstream userinfo quota probe result")
		}
		cloned := *result
		return &cloned, nil
	}
}

// RefreshForScheduling ensures a recognized USTC /key/info account has a recent
// quota snapshot, returning the snapshot overlaid on a copy of the supplied
// account. Only upstream_userinfo_ Extra keys are refreshed.
func (s *UpstreamUserInfoQuotaService) RefreshForScheduling(ctx context.Context, account *Account) (*Account, error) {
	if account == nil {
		return nil, infraerrors.New(http.StatusNotFound, "USERINFO_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	accountCopy := copyAccountForUserInfoQuotaRefresh(account)
	if !account.SupportsUserInfoQuota() {
		return accountCopy, nil
	}
	now := time.Now().UTC()
	incomingUpdatedAt, hasIncomingUpdatedAt := userInfoQuotaExtraUpdatedAt(account.Extra)
	forceRefresh := userInfoQuotaResetElapsed(account.Extra, now)
	if userInfoQuotaExtraIsFresh(account.Extra, now) && !forceRefresh {
		if s != nil {
			if snapshot, ok := s.newerCachedSchedulingRefresh(account.ID, now, incomingUpdatedAt); ok {
				return overlayUserInfoQuotaSnapshot(accountCopy, snapshot), nil
			}
		}
		return accountCopy, nil
	}
	if s == nil {
		return accountCopy, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_NOT_CONFIGURED", "upstream userinfo quota service is not configured")
	}

	if snapshot, err, done := s.cachedSchedulingRefresh(account.ID, now, forceRefresh, incomingUpdatedAt, hasIncomingUpdatedAt); done {
		return overlayUserInfoQuotaSnapshot(accountCopy, snapshot), err
	}

	resultCh := s.startSchedulingRefresh(accountCopy, forceRefresh, incomingUpdatedAt, hasIncomingUpdatedAt)
	select {
	case <-ctx.Done():
		return accountCopy, ctx.Err()
	case flightResult := <-resultCh:
		if flightResult.Err != nil {
			return accountCopy, flightResult.Err
		}
		result, ok := flightResult.Val.(*userInfoQuotaRefreshFlightResult)
		if !ok || result == nil {
			return accountCopy, infraerrors.New(http.StatusInternalServerError, "USERINFO_QUOTA_RESULT_INVALID", "invalid scheduling quota refresh result")
		}
		return overlayUserInfoQuotaSnapshot(accountCopy, result.snapshot), result.err
	}
}

// QuotaForAdmission returns immediately with the best available account copy.
// Cold or stale snapshots start a per-account refresh in the shared singleflight
// and report ready=false unless the default pool permits eligible observations
// during background refresh. Known unavailable quotas are never bypassed.
func (s *UpstreamUserInfoQuotaService) QuotaForAdmission(ctx context.Context, account *Account) (*Account, bool) {
	if account == nil {
		return nil, false
	}
	accountCopy := copyAccountForUserInfoQuotaRefresh(account)
	if ctx != nil && ctx.Err() != nil {
		return accountCopy, false
	}
	if !account.SupportsUserInfoQuota() {
		return accountCopy, true
	}
	now := time.Now().UTC()
	incomingUpdatedAt, hasIncomingUpdatedAt := userInfoQuotaExtraUpdatedAt(account.Extra)
	_, limitsKnown := USTCAccountLimits(account)
	forceRefresh := userInfoQuotaResetElapsed(account.Extra, now) || (isDefaultUSTCAccount(account) && !limitsKnown)
	if userInfoQuotaExtraIsFresh(account.Extra, now) && !forceRefresh {
		if s != nil {
			if snapshot, ok := s.newerCachedSchedulingRefresh(account.ID, now, incomingUpdatedAt); ok {
				return overlayUserInfoQuotaSnapshot(accountCopy, snapshot), true
			}
		}
		return accountCopy, true
	}
	if s == nil {
		return accountCopy, false
	}

	if snapshot, _, done := s.cachedSchedulingRefresh(account.ID, now, forceRefresh, incomingUpdatedAt, hasIncomingUpdatedAt); done {
		if len(snapshot) > 0 {
			accountCopy = overlayUserInfoQuotaSnapshot(accountCopy, snapshot)
		}
		// A completed failed probe uses the existing fail-open policy for unknown
		// usage during backoff. Known exhausted/invalid snapshots still fail the
		// admission eligibility gate.
		return accountCopy, true
	}

	// DoChan starts the callback asynchronously. The callback and the synchronous
	// RefreshForScheduling path use the same key and share one upstream refresh.
	s.ensureSchedulingRefresh(accountCopy, forceRefresh, incomingUpdatedAt, hasIncomingUpdatedAt)
	if ctx != nil {
		if background, _ := ctx.Value(ustcQuotaBackgroundAdmissionKey{}).(bool); background && userInfoQuotaSchedulingFailureReason(accountCopy, now) == "" {
			return accountCopy, true
		}
	}
	return accountCopy, false
}

// Polling waiters need one background subscriber per account, not a new
// singleflight result channel on every poll while the upstream is slow.
func (s *UpstreamUserInfoQuotaService) ensureSchedulingRefresh(account *Account, forceRefresh bool, forceAfter time.Time, hasForceAfter bool) {
	s.refreshMu.Lock()
	if s.refreshPending == nil {
		s.refreshPending = make(map[int64]bool)
	}
	if s.refreshPending[account.ID] || len(s.refreshPending) >= userInfoQuotaSchedulingCacheMax {
		s.refreshMu.Unlock()
		return
	}
	s.refreshPending[account.ID] = true
	s.refreshMu.Unlock()
	resultCh := s.startSchedulingRefresh(account, forceRefresh, forceAfter, hasForceAfter)
	go func() {
		<-resultCh
		s.refreshMu.Lock()
		delete(s.refreshPending, account.ID)
		s.refreshMu.Unlock()
		if s.onRefresh != nil {
			s.onRefresh()
		}
	}()
}

func (s *UpstreamUserInfoQuotaService) startSchedulingRefresh(account *Account, forceRefresh bool, forceAfter time.Time, hasForceAfter bool) <-chan singleflight.Result {
	queryAccount := copyAccountForUserInfoQuotaRefresh(account)
	key := "userinfo_quota_scheduling:" + strconv.FormatInt(account.ID, 10)
	return s.refreshFlight.DoChan(key, func() (any, error) {
		if snapshot, err, done := s.cachedSchedulingRefresh(queryAccount.ID, time.Now().UTC(), forceRefresh, forceAfter, hasForceAfter); done {
			return &userInfoQuotaRefreshFlightResult{snapshot: snapshot, err: err}, nil
		}
		return s.refreshSchedulingSnapshot(queryAccount), nil
	})
}

func (s *UpstreamUserInfoQuotaService) newerCachedSchedulingRefresh(accountID int64, now, incomingUpdatedAt time.Time) (map[string]any, bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	entry := s.getRefreshCacheEntryLocked(accountID, now, false)
	if entry == nil {
		return nil, false
	}
	if entry.snapshot == nil || entry.successAt.IsZero() || !entry.successAt.After(incomingUpdatedAt) ||
		now.Sub(entry.successAt) < 0 || now.Sub(entry.successAt) > userInfoQuotaSchedulingFreshness ||
		userInfoQuotaResetElapsed(entry.snapshot, now) {
		return nil, false
	}
	return cloneUserInfoQuotaSnapshot(entry.snapshot), true
}

func (s *UpstreamUserInfoQuotaService) cachedSchedulingRefresh(accountID int64, now time.Time, forceRefresh bool, forceAfter time.Time, hasForceAfter bool) (map[string]any, error, bool) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	entry := s.getRefreshCacheEntryLocked(accountID, now, false)
	if entry == nil {
		return nil, nil, false
	}
	forceStillRequired := forceRefresh && (!hasForceAfter || entry.successAt.IsZero() || entry.successAt.Before(forceAfter))
	if !forceStillRequired && entry.snapshot != nil && !userInfoQuotaResetElapsed(entry.snapshot, now) &&
		!entry.successAt.IsZero() && now.Sub(entry.successAt) >= 0 && now.Sub(entry.successAt) <= userInfoQuotaSchedulingFreshness {
		return cloneUserInfoQuotaSnapshot(entry.snapshot), nil, true
	}
	if entry.lastError != nil && !entry.lastAttempt.IsZero() && now.Sub(entry.lastAttempt) >= 0 &&
		now.Sub(entry.lastAttempt) < userInfoQuotaSchedulingBackoff {
		return cloneUserInfoQuotaSnapshot(entry.snapshot), entry.lastError, true
	}
	return nil, nil, false
}

func (s *UpstreamUserInfoQuotaService) refreshSchedulingSnapshot(account *Account) *userInfoQuotaRefreshFlightResult {
	s.refreshProbeOnce.Do(func() {
		s.refreshProbeSlots = make(chan struct{}, userInfoQuotaSchedulingMaxProbes)
	})
	s.refreshProbeSlots <- struct{}{}
	defer func() { <-s.refreshProbeSlots }()
	accountID := account.ID
	now := time.Now().UTC()
	s.refreshMu.Lock()
	entry := s.getRefreshCacheEntryLocked(accountID, now, true)
	entry.lastAttempt = now
	entry.lastError = nil
	entry.lastAccessAt = now
	s.refreshMu.Unlock()

	result, err := s.QueryQuotaForAccount(context.Background(), account)
	if err == nil && (result == nil || !result.Success || result.Error != "") {
		message := "upstream quota refresh did not return a successful snapshot"
		if result != nil && result.Error != "" {
			message = result.Error
		}
		err = errors.New(message)
	}
	if err != nil {
		slog.Warn("ustc_scheduling_quota_refresh_failed", "account_id", accountID, "error", err)
		s.refreshMu.Lock()
		failedAt := time.Now().UTC()
		entry := s.getRefreshCacheEntryLocked(accountID, failedAt, true)
		entry.lastAttempt = failedAt
		entry.lastError = err
		entry.lastAccessAt = failedAt
		snapshot := cloneUserInfoQuotaSnapshot(entry.snapshot)
		s.refreshMu.Unlock()
		return &userInfoQuotaRefreshFlightResult{snapshot: snapshot, err: err}
	}

	snapshot := userInfoQuotaSnapshotFromResult(result)
	successAt := time.Unix(result.FetchedAt, 0).UTC()
	if result.FetchedAt <= 0 {
		successAt = time.Now().UTC()
	}
	s.refreshMu.Lock()
	entry = s.getRefreshCacheEntryLocked(accountID, time.Now().UTC(), true)
	entry.snapshot = cloneUserInfoQuotaSnapshot(snapshot)
	entry.successAt = successAt
	entry.lastError = nil
	entry.lastAccessAt = time.Now().UTC()
	s.refreshMu.Unlock()
	return &userInfoQuotaRefreshFlightResult{snapshot: snapshot}
}

func (s *UpstreamUserInfoQuotaService) getRefreshCacheEntryLocked(accountID int64, now time.Time, create bool) *userInfoQuotaRefreshCacheEntry {
	if s.refreshCache == nil {
		if !create {
			return nil
		}
		s.refreshCache = make(map[int64]*userInfoQuotaRefreshCacheEntry)
	}
	if s.refreshLRU == nil {
		s.refreshLRU = list.New()
	}
	s.pruneIdleRefreshCacheEntriesLocked(now, 16)
	entry := s.refreshCache[accountID]
	if entry != nil && userInfoQuotaRefreshEntryIdle(entry, now) {
		s.removeRefreshCacheEntryLocked(accountID, entry)
		entry = nil
	}
	if entry == nil && create {
		for len(s.refreshCache) >= userInfoQuotaSchedulingCacheMax {
			if !s.evictLeastRecentlyUsedRefreshCacheEntryLocked() {
				break
			}
		}
		entry = &userInfoQuotaRefreshCacheEntry{}
		s.refreshCache[accountID] = entry
	}
	if entry != nil {
		entry.lastAccessAt = now
		if entry.lruElement == nil {
			entry.lruElement = s.refreshLRU.PushFront(accountID)
		} else {
			s.refreshLRU.MoveToFront(entry.lruElement)
		}
	}
	return entry
}

func userInfoQuotaRefreshEntryIdle(entry *userInfoQuotaRefreshCacheEntry, now time.Time) bool {
	return entry == nil || (!entry.lastAccessAt.IsZero() && now.Sub(entry.lastAccessAt) > userInfoQuotaSchedulingCacheIdle)
}

func (s *UpstreamUserInfoQuotaService) pruneIdleRefreshCacheEntriesLocked(now time.Time, budget int) {
	for pruned := 0; pruned < budget; pruned++ {
		element := s.refreshLRU.Back()
		if element == nil {
			return
		}
		accountID, ok := element.Value.(int64)
		if !ok {
			s.refreshLRU.Remove(element)
			continue
		}
		entry := s.refreshCache[accountID]
		if !userInfoQuotaRefreshEntryIdle(entry, now) {
			return
		}
		if entry == nil {
			s.refreshLRU.Remove(element)
			delete(s.refreshCache, accountID)
			continue
		}
		s.removeRefreshCacheEntryLocked(accountID, entry)
	}
}

func (s *UpstreamUserInfoQuotaService) removeRefreshCacheEntryLocked(accountID int64, entry *userInfoQuotaRefreshCacheEntry) {
	delete(s.refreshCache, accountID)
	if entry != nil && entry.lruElement != nil {
		s.refreshLRU.Remove(entry.lruElement)
		entry.lruElement = nil
	}
}

func (s *UpstreamUserInfoQuotaService) evictLeastRecentlyUsedRefreshCacheEntryLocked() bool {
	element := s.refreshLRU.Back()
	if element == nil {
		return false
	}
	accountID, ok := element.Value.(int64)
	if !ok {
		s.refreshLRU.Remove(element)
		return true
	}
	entry := s.refreshCache[accountID]
	if entry == nil {
		s.refreshLRU.Remove(element)
		delete(s.refreshCache, accountID)
	} else {
		s.removeRefreshCacheEntryLocked(accountID, entry)
	}
	return true
}

func userInfoQuotaExtraIsFresh(extra map[string]any, now time.Time) bool {
	updatedAt, ok := userInfoQuotaExtraUpdatedAt(extra)
	if !ok {
		return false
	}
	age := now.Sub(updatedAt)
	return age >= 0 && age <= userInfoQuotaSchedulingFreshness
}

func userInfoQuotaExtraUpdatedAt(extra map[string]any) (time.Time, bool) {
	value, ok := extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated)].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	updatedAt, err := parseUserInfoTime(value)
	return updatedAt, err == nil
}

func userInfoQuotaResetElapsed(extra map[string]any, now time.Time) bool {
	for _, window := range userInfoQuotaWindowsForScheduling(extra) {
		if !userInfoQuotaWindowExhausted(window) || window.ResetAt == "" {
			continue
		}
		if resetAt, err := parseUserInfoTime(window.ResetAt); err == nil && !resetAt.After(now) {
			return true
		}
	}
	return false
}

func userInfoQuotaSnapshotFromResult(result *UserInfoQuotaResult) map[string]any {
	updatedAt := time.Unix(result.FetchedAt, 0).UTC()
	if result.FetchedAt <= 0 {
		updatedAt = time.Now().UTC()
	}
	snapshot := map[string]any{
		UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget):   result.MaxBudget,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend):    result.Spend,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixRemain):   result.Remaining,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixValid):    result.Valid,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixUpdated):  updatedAt.Format(time.RFC3339),
		UserInfoQuotaExtraKey(UserInfoExtraSuffixExpires):  result.ExpiresAt,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixResetAt):  result.BudgetResetAt,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixKeyAlias): result.KeyAlias,
		UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows):  append([]UserInfoBudgetWindow(nil), result.Windows...),
	}
	// Keep the last valid limits if a successful budget response omits metadata.
	if result.LimitsKnown {
		pointerValue := func(value *int) any {
			if value == nil {
				return nil
			}
			return *value
		}
		snapshot[UserInfoQuotaExtraKey(UserInfoExtraSuffixRPM)] = pointerValue(result.RPMLimit)
		snapshot[UserInfoQuotaExtraKey(UserInfoExtraSuffixParallel)] = pointerValue(result.MaxParallelRequests)
		snapshot[UserInfoQuotaExtraKey(UserInfoExtraSuffixTPM)] = pointerValue(result.TPMLimit)
		snapshot[UserInfoQuotaExtraKey(UserInfoExtraSuffixLimitsKnown)] = true
	}
	return snapshot
}

func overlayUserInfoQuotaSnapshot(account *Account, snapshot map[string]any) *Account {
	if account == nil {
		return nil
	}
	if len(snapshot) == 0 {
		return account
	}
	// Cached observations, including a failure fallback, must never roll back a
	// newer snapshot fetched by another instance. Probe timestamps have second
	// precision, so an authoritative result may replace an equal-time snapshot.
	targetAt, targetKnown := userInfoQuotaExtraUpdatedAt(account.Extra)
	snapshotAt, snapshotKnown := userInfoQuotaExtraUpdatedAt(snapshot)
	if targetKnown && (!snapshotKnown || snapshotAt.Before(targetAt)) {
		return account
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any, len(snapshot))
	}
	for key, value := range snapshot {
		if strings.HasPrefix(key, userInfoExtraPrefix) {
			account.Extra[key] = cloneUserInfoQuotaExtraValue(value)
		}
	}
	return account
}

func copyAccountForUserInfoQuotaRefresh(account *Account) *Account {
	if account == nil {
		return nil
	}
	cloned := *account
	cloned.Credentials = cloneStringAnyMap(account.Credentials)
	cloned.Extra = cloneStringAnyMap(account.Extra)
	if windows, ok := cloned.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)].([]UserInfoBudgetWindow); ok {
		cloned.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = append([]UserInfoBudgetWindow(nil), windows...)
	}
	cloned.modelMappingCache = nil
	cloned.modelMappingCacheReady = false
	cloned.headerOverrideCache = nil
	cloned.headerOverrideCacheReady = false
	return &cloned
}

func cloneStringAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneUserInfoQuotaSnapshot(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneUserInfoQuotaExtraValue(value)
	}
	return cloned
}

func cloneUserInfoQuotaExtraValue(value any) any {
	if windows, ok := value.([]UserInfoBudgetWindow); ok {
		return append([]UserInfoBudgetWindow(nil), windows...)
	}
	return value
}

func (s *UpstreamUserInfoQuotaService) queryQuotaForAccount(ctx context.Context, account *Account) (*UserInfoQuotaResult, error) {
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(account.GetCredential("access_token"))
	}
	if apiKey == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_NO_APIKEY", "account api_key is empty")
	}

	targetURL := userInfoQuotaURL(account.userInfoQuotaBaseURL())
	if targetURL == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_NO_URL", "account base_url is empty")
	}
	// 出站 URL 安全策略（与网关转发/CN 探测同一套校验）。
	validatedURL, err := cnValidateProbeURL(s.cfg, targetURL)
	if err != nil {
		return nil, infraerrors.New(http.StatusForbidden, "USERINFO_QUOTA_URL_REJECTED", err.Error())
	}
	targetURL = validatedURL

	proxyURL := s.resolveProxyURL(ctx, account)
	callCtx, cancel := context.WithTimeout(ctx, userInfoQuotaUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "USERINFO_QUOTA_REQUEST_BUILD_FAILED", "build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "USERINFO_QUOTA_REQUEST_FAILED", "upstream request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, userInfoQuotaMaxBodyBytes))

	now := time.Now().UTC()
	result := &UserInfoQuotaResult{
		Provider:   "upstream_userinfo",
		FetchedAt:  now.Unix(),
		StatusCode: resp.StatusCode,
		Unit:       "CNY",
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		result.Error = fmt.Sprintf("Authentication failed (HTTP %d)", resp.StatusCode)
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("API error (HTTP %d): %s", resp.StatusCode, truncate(strings.TrimSpace(string(bodyBytes)), 240))
		return result, nil
	}

	// LiteLLM /key/info 形如 {"key": "<hash>", "info": {...}}，预算字段在 info 下；
	// 兼容顶层直接给预算字段的变体。形状无法识别时按错误处理，
	// 不能落成「不限额」快照覆盖上一次真实数据。
	root := gjson.ParseBytes(bodyBytes)
	keyNode := userInfoKeyNode(root)
	if !keyNode.IsObject() {
		result.Error = "unrecognized /key/info response shape"
		return result, nil
	}

	spend := parseUserInfoF64(keyNode.Get("spend"))

	// 多层预算：顶层 max_budget + budget_limits[] 全部窗口都要展示。
	// 主窗口（budget_duration 与 spend 同口径）直接用 spend；
	// 短窗口从 budget_limits_usage[duration].current_spend 直读。
	primaryDuration := strings.TrimSpace(keyNode.Get("budget_duration").String())
	maxBudget, remaining, resetAt, budgetWindows := userInfoBudgetWindows(keyNode, spend, primaryDuration)

	// LiteLLM 用户级预算（如 24h）挂在 /user/info.user_info 上，key 的 /key/info
	// 只有短窗 budget_limits 且 max_budget 为 null。24h 必须从用户级补，否则永远不显示。
	if userWindow := s.fetchUserInfoBudgetWindow(ctx, account, apiKey); userWindow != nil {
		budgetWindows = userInfoMergeUserWindow(budgetWindows, *userWindow)
		// 合并后按 limit 升序重排（3h → 12h → 24h）。
		sort.SliceStable(budgetWindows, func(i, j int) bool { return budgetWindows[i].Limit < budgetWindows[j].Limit })
		// 用户级 24h 若比 key 侧更松/更紧，约束档可能变化，重算 remaining。
		maxBudget, remaining, resetAt = userInfoConstraint(budgetWindows)
	}

	blocked := keyNode.Get("blocked").Bool()
	expiresRaw := strings.TrimSpace(keyNode.Get("expires").String())
	if expiresRaw == "" {
		expiresRaw = strings.TrimSpace(keyNode.Get("expires_at").String())
	}
	// 无到期时间：未封禁即可用（预算型 key 可能永久有效）；
	// 到期时间无法解析时同样只看 blocked，避免误杀。
	valid := !blocked
	if expiresRaw != "" {
		if t, err := parseUserInfoTime(expiresRaw); err == nil {
			valid = !blocked && t.After(now)
			result.ExpiresAt = t.UTC().Format(time.RFC3339)
		}
	}

	result.Success = true
	var rpmKnown, parallelKnown bool
	result.RPMLimit, rpmKnown = parseUSTCLimit(keyNode.Get("rpm_limit"))
	result.MaxParallelRequests, parallelKnown = parseUSTCLimit(keyNode.Get("max_parallel_requests"))
	result.TPMLimit, _ = parseUSTCLimit(keyNode.Get("tpm_limit"))
	result.LimitsKnown = rpmKnown && parallelKnown
	result.MaxBudget = maxBudget
	result.Spend = spend
	result.Remaining = remaining
	result.Valid = valid
	result.Blocked = blocked
	result.BudgetResetAt = resetAt
	result.KeyAlias = strings.TrimSpace(keyNode.Get("key_alias").String())
	result.Windows = budgetWindows

	// 全部键无条件写入：UpdateExtra 用 JSONB || 合并，不在 updates 里的键
	// 会永久残留，上游不再返回 expires/reset_at/windows 时必须用空值覆盖掉旧快照。
	updates := userInfoQuotaSnapshotFromResult(result)
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		slog.Warn("userinfo_quota_persist_failed", "account_id", account.ID, "error", err)
	} else {
		result.Persisted = true
	}
	return result, nil
}

// userInfoKeyNode 从 /key/info 响应里取出预算字段所在节点。
// /key/info 形如 {"key": "<hash>", "info": {...}}；兼容顶层直接给字段的变体。
// 节点必须能识别出预算/身份字段，否则返回空 Result（响应形状异常，
// 不能落成「不限额」快照）。
func userInfoKeyNode(root gjson.Result) gjson.Result {
	if !root.IsObject() {
		return gjson.Result{}
	}
	node := root.Get("info")
	if !node.IsObject() {
		// info 为 null / 缺失时回退顶层（gjson 对 null 的 Exists() 为 true，
		// 必须用 IsObject 判定）。
		node = root
	}
	if node.Get("spend").Exists() || node.Get("max_budget").Exists() ||
		node.Get("key_alias").Exists() || node.Get("budget_duration").Exists() ||
		node.Get("budget_limits").Exists() || node.Get("rpm_limit").Exists() {
		return node
	}
	return gjson.Result{}
}

// Explicit null means unlimited; absent or malformed data means unknown.
func parseUSTCLimit(node gjson.Result) (*int, bool) {
	if !node.Exists() {
		return nil, false
	}
	if node.Type == gjson.Null {
		return nil, true
	}
	value := parseUserInfoF64(node)
	if value <= 0 || math.Trunc(value) != value || value > math.MaxInt32 {
		return nil, false
	}
	limit := int(value)
	return &limit, true
}

// parseUserInfoF64 解析 JSON 数值或字符串为 float64（兼容 "100" 与 100）。
func parseUserInfoF64(node gjson.Result) float64 {
	if !node.Exists() {
		return 0
	}
	switch node.Type {
	case gjson.Number:
		return node.Num
	case gjson.String:
		s := strings.TrimSpace(node.Str)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

// parseUserInfoTime 解析 LiteLLM 的时间字段。
// 兼容 RFC3339 / RFC3339Nano、带毫秒与 +08:00 偏移的写法
// （如 "2053-11-04T13:46:10.113+08:00"），以及 Unix 时间戳（秒或毫秒）。
func parseUserInfoTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if f, err := strconv.ParseFloat(raw, 64); err == nil && f > 0 {
		// 大于 1e12 按毫秒解析，否则按秒。
		if f > 1e12 {
			return time.UnixMilli(int64(f)).UTC(), nil
		}
		return time.Unix(int64(f), 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", raw)
}

// userInfoBudgetWindows 收集全部预算窗口，按窗口内用量算剩余。
//
// 主窗口（顶层 max_budget + budget_duration，与 spend 同口径）直接用 spend；
// budget_limits[] 的短窗口从 budget_limits_usage[duration].current_spend 直读
// —— LiteLLM 已对外暴露各窗口独立计数器。上游未提供时 UsedKnown=false。
//
// remaining / maxBudget 取「剩余最少」的窗口（任一档触顶即不可用）。
func userInfoBudgetWindows(keyNode gjson.Result, spend float64, primaryDuration string) (maxBudget, remaining float64, resetAt string, windows []UserInfoBudgetWindow) {
	type rawWindow struct {
		duration string
		limit    float64
		resetAt  string
		primary  bool
	}
	var raws []rawWindow

	add := func(limitNode, resetNode, durationNode gjson.Result, primary bool) {
		if !limitNode.Exists() || limitNode.Type == gjson.Null {
			return
		}
		limit := parseUserInfoF64(limitNode)
		if limit <= 0 {
			return
		}
		w := rawWindow{limit: limit, primary: primary}
		if durationNode.Exists() && durationNode.Type != gjson.Null {
			w.duration = strings.TrimSpace(durationNode.String())
		}
		if resetNode.Exists() && resetNode.Type != gjson.Null {
			if t, err := parseUserInfoTime(resetNode.String()); err == nil {
				w.resetAt = t.UTC().Format(time.RFC3339)
			}
		}
		raws = append(raws, w)
	}

	add(keyNode.Get("max_budget"), keyNode.Get("budget_reset_at"), keyNode.Get("budget_duration"), true)
	if limits := keyNode.Get("budget_limits"); limits.IsArray() {
		limits.ForEach(func(_, item gjson.Result) bool {
			add(item.Get("max_budget"), item.Get("reset_at"), item.Get("budget_duration"), false)
			return true
		})
	}

	if len(raws) == 0 {
		return 0, 0, "", nil
	}

	// 按 limit 升序展示（3h → 12h → 24h），最紧的在前。
	// SliceStable：同 limit 的窗口保持相对顺序，避免前端按 index 取色抖动。
	sort.SliceStable(raws, func(i, j int) bool { return raws[i].limit < raws[j].limit })

	usage := keyNode.Get("budget_limits_usage")
	windows = make([]UserInfoBudgetWindow, 0, len(raws))
	for _, r := range raws {
		windowSpend, known := userInfoWindowSpend(r.primary, r.duration, spend, primaryDuration, usage)
		remain := 0.0
		usedPercent := 0.0
		if known {
			remain = math.Round((r.limit-windowSpend)*100) / 100
			if remain < 0 {
				remain = 0
			}
			usedPercent = math.Round(windowSpend/r.limit*1000) / 10
		}
		windows = append(windows, UserInfoBudgetWindow{
			Duration:    r.duration,
			Limit:       r.limit,
			Remaining:   remain,
			UsedPercent: usedPercent,
			WindowSpend: windowSpend,
			UsedKnown:   known,
			ResetAt:     r.resetAt,
		})
	}

	// 「剩余最少」的已知窗口作为约束档；都未知时退回第一档的 limit。
	maxBudget, remaining, resetAt = userInfoConstraint(windows)
	return maxBudget, remaining, resetAt, windows
}

// userInfoWindowSpend 计算单窗口的窗口内消耗。
//
// 主窗口与 spend 同口径，直接返回 spend；
// 短窗口从 budget_limits_usage[duration].current_spend 直读（LiteLLM 已暴露
// 各窗口独立计数器）。上游未提供该字段时 known=false，不给百分比。
func userInfoWindowSpend(primary bool, duration string, spend float64, primaryDuration string, usage gjson.Result) (windowSpend float64, known bool) {
	if primary || duration == "" || duration == primaryDuration {
		return spend, true
	}
	u := usage.Get(duration)
	if !u.IsObject() && u.Type != gjson.Number && u.Type != gjson.String {
		return 0, false
	}
	// 形如 {"current_spend": 2.01}；兼容直接给数值的变体。
	if node := u.Get("current_spend"); node.Exists() {
		ws := parseUserInfoF64(node)
		if ws < 0 {
			ws = 0
		}
		return ws, true
	}
	if u.Type == gjson.Number || u.Type == gjson.String {
		ws := parseUserInfoF64(u)
		if ws < 0 {
			ws = 0
		}
		return ws, true
	}
	return 0, false
}

// userInfoConstraint 从窗口列表取「剩余最少」的已知窗口作为约束档（任一档触顶即不可用）。
// userInfoBudgetWindows 与合并用户级窗口后的重算都走这里。
func userInfoConstraint(windows []UserInfoBudgetWindow) (maxBudget, remaining float64, resetAt string) {
	if len(windows) == 0 {
		return 0, 0, ""
	}
	best := windows[0]
	bestRemain := math.Inf(1)
	for _, w := range windows {
		if !w.UsedKnown {
			continue
		}
		if rem := w.Limit - w.WindowSpend; rem < bestRemain {
			best, bestRemain = w, rem
		}
	}
	if math.IsInf(bestRemain, 1) {
		return best.Limit, best.Limit, best.ResetAt
	}
	remaining = math.Round(bestRemain*100) / 100
	if remaining < 0 {
		remaining = 0
	}
	return best.Limit, remaining, best.ResetAt
}

// userInfoMergeUserWindow 把用户级预算窗口（如 24h）并入 key 侧窗口列表。
// 同 duration 时：两侧都已知则视为同一份预算的两种写法，保留 key 级不重复；
// key 侧 used_known=false 时用用户级那份已知数据顶上，避免 24h 退化成 chip。
func userInfoMergeUserWindow(windows []UserInfoBudgetWindow, user UserInfoBudgetWindow) []UserInfoBudgetWindow {
	if user.Limit <= 0 {
		return windows
	}
	for i, w := range windows {
		if w.Duration == user.Duration && user.Duration != "" {
			if !w.UsedKnown && user.UsedKnown {
				windows[i] = user
			}
			return windows
		}
	}
	return append(windows, user)
}

// fetchUserInfoBudgetWindow 拉取 /user/info 的用户级预算窗口（如 24h）。
// 拉取失败或无用户级预算时返回 nil，不影响 key 侧已解析的短窗。
func (s *UpstreamUserInfoQuotaService) fetchUserInfoBudgetWindow(ctx context.Context, account *Account, apiKey string) *UserInfoBudgetWindow {
	targetURL := userInfoUserURL(account.userInfoQuotaBaseURL())
	if targetURL == "" {
		return nil
	}
	validatedURL, err := cnValidateProbeURL(s.cfg, targetURL)
	if err != nil {
		return nil
	}
	proxyURL := s.resolveProxyURL(ctx, account)
	callCtx, cancel := context.WithTimeout(ctx, userInfoQuotaUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, validatedURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, maxInt(account.Concurrency, 1))
	if err != nil {
		slog.Warn("userinfo_user_budget_fetch_failed", "account_id", account.ID, "error", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("userinfo_user_budget_http_error", "account_id", account.ID, "status", resp.StatusCode)
		return nil
	}
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, userInfoQuotaMaxBodyBytes))
	root := gjson.ParseBytes(bodyBytes)
	userNode := root.Get("user_info")
	if !userNode.IsObject() {
		userNode = root
	}
	return userInfoUserBudgetWindow(userNode)
}

// userInfoUserBudgetWindow 从 /user/info 的 user_info 节点解析用户级预算窗口。
// 形如 max_budget=100, budget_duration="24h", spend=0.39, budget_reset_at=...
// 无 max_budget 或 budget_duration 时返回 nil。
func userInfoUserBudgetWindow(userNode gjson.Result) *UserInfoBudgetWindow {
	limit := parseUserInfoF64(userNode.Get("max_budget"))
	if limit <= 0 {
		return nil
	}
	duration := strings.TrimSpace(userNode.Get("budget_duration").String())
	if duration == "" {
		return nil
	}
	spend := parseUserInfoF64(userNode.Get("spend"))
	if spend < 0 {
		spend = 0
	}
	remain := math.Round((limit-spend)*100) / 100
	if remain < 0 {
		remain = 0
	}
	usedPercent := math.Round(spend/limit*1000) / 10
	w := &UserInfoBudgetWindow{
		Duration:    duration,
		Limit:       limit,
		Remaining:   remain,
		UsedPercent: usedPercent,
		WindowSpend: spend,
		UsedKnown:   true,
	}
	if resetRaw := strings.TrimSpace(userNode.Get("budget_reset_at").String()); resetRaw != "" {
		if t, err := parseUserInfoTime(resetRaw); err == nil {
			w.ResetAt = t.UTC().Format(time.RFC3339)
		}
	}
	return w
}

func (s *UpstreamUserInfoQuotaService) loadAccount(ctx context.Context, accountID int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusNotFound, "USERINFO_QUOTA_ACCOUNT_NOT_FOUND", "account not found: %v", err)
	}
	if err := validateUserInfoQuotaAccount(account); err != nil {
		return nil, err
	}
	return account, nil
}

func validateUserInfoQuotaAccount(account *Account) error {
	if account == nil {
		return infraerrors.New(http.StatusNotFound, "USERINFO_QUOTA_ACCOUNT_NOT_FOUND", "account not found")
	}
	if !account.SupportsUserInfoQuota() {
		return infraerrors.New(http.StatusBadRequest, "USERINFO_QUOTA_UNSUPPORTED", "account is not a recognized /key/info quota upstream")
	}
	return nil
}

func (s *UpstreamUserInfoQuotaService) resolveProxyURL(ctx context.Context, account *Account) string {
	if account == nil || account.ProxyID == nil {
		return ""
	}
	if account.Proxy != nil {
		return account.Proxy.URL()
	}
	if s != nil && s.proxyRepo != nil {
		if proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID); err == nil && proxy != nil {
			account.Proxy = proxy
			return proxy.URL()
		}
	}
	return ""
}
