package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
)

type ustcAdmissionContextKey struct{}
type ustcBoundAccountKey struct{}

// Continuation IDs belong to one upstream Key. Temporary capacity pressure must
// wait/reject on that Key rather than silently moving the continuation elsewhere.
func (s *OpenAIGatewayService) selectUSTCPreviousResponse(ctx context.Context, groupID *int64, responseID, sessionHash, model string, excluded map[int64]struct{}, compact bool, capability OpenAIEndpointCapability, imageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport) (*AccountSelectionResult, bool, error) {
	if strings.TrimSpace(responseID) == "" {
		return nil, false, nil
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return nil, false, nil
	}
	id, err := store.GetResponseAccount(ctx, derefGroupID(groupID), strings.TrimSpace(responseID))
	if err != nil || id <= 0 {
		return nil, false, nil
	}
	account, err := s.loadResponseBindingAccount(ctx, id)
	if err != nil || !isDefaultUSTCAccount(account) {
		return nil, false, nil
	}
	if requiredTransport != OpenAIUpstreamTransportAny && requiredTransport != OpenAIUpstreamTransportHTTPSSE && requiredTransport != OpenAIUpstreamTransportResponsesWebsocketV2Ingress {
		return nil, true, fmt.Errorf("%w: USTC native Responses WebSocket transport is unsupported; use the HTTP/SSE bridge", ErrNoAvailableAccounts)
	}
	if requiredTransport == OpenAIUpstreamTransportResponsesWebsocketV2Ingress && !s.isOpenAIAccountTransportCompatible(account, requiredTransport) {
		return nil, true, fmt.Errorf("%w: USTC WebSocket ingress is disabled or unsupported for this account", ErrNoAvailableAccounts)
	}
	if s.checkChannelPricingRestriction(ctx, groupID, model) || !s.openAIAccountMatchesSchedulingGroup(account, groupID) {
		return nil, true, ErrNoAvailableAccounts
	}
	if !account.SupportsOpenAIImageCapability(imageCapability) {
		return nil, true, ErrNoAvailableAccounts
	}
	ctx = context.WithValue(ctx, ustcBoundAccountKey{}, true)
	selection, err := s.selectBalancedDefaultUSTCAccountWithWait(ctx, groupID, []Account{*account}, sessionHash, model, excluded, compact, capability, false)
	if err == nil && selection != nil && selection.Account != nil && !selection.Account.SupportsOpenAIImageCapability(imageCapability) {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return nil, true, ErrNoAvailableAccounts
	}
	return selection, true, err
}

// A reservation belongs to one actual upstream attempt, not one client request.
type ustcAdmission struct {
	mu                  sync.Mutex
	cache               USTCCapacityCache
	ticket              *USTCTicket
	limits              USTCLimits
	origin              context.Context
	committed, released bool
	renewer             *ustcLeaseRenewer
	notify              func()
}

func (r *ustcAdmission) commit(ctx context.Context) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released || r.committed || ctx.Err() != nil || (r.origin != nil && r.origin.Err() != nil) {
		return false
	}
	allowed, err := r.cache.USTCCommit(ctx, r.ticket, r.limits)
	if err != nil {
		slog.Warn("ustc_admission_commit_failed", "error", err)
	}
	if !allowed || err != nil {
		return false
	}
	r.committed = true
	if r.renewer != nil {
		r.renewer.register(r.ticket)
	}
	return true
}

func (r *ustcAdmission) release() {
	r.releaseWithNotification(true, false)
}

func (r *ustcAdmission) releaseWithNotification(notify, pendingOnly bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.released || (pendingOnly && r.committed) {
		r.mu.Unlock()
		return
	}
	r.released = true
	if r.renewer != nil {
		r.renewer.unregister(r.ticket)
	}
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.cache.USTCRelease(ctx, r.ticket); err != nil {
		slog.Warn("ustc_admission_release_failed", "error", err)
	}
	if notify && r.notify != nil {
		r.notify()
	}
}

func (r *AccountSelectionResult) ReleaseUSTCAdmission() {
	if r != nil {
		// Forwarding can outlive a disconnected client. Once committed, only
		// the transport/body lifecycle may release the actual upstream lease.
		r.ustcAdmission.releaseWithNotification(true, true)
	}
}

// ReleaseWithUSTCAdmission keeps pending-ticket cleanup and slot cleanup together.
// A committed ticket remains owned by the upstream response body.
func (r *AccountSelectionResult) ReleaseWithUSTCAdmission(releaseSlot func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.ReleaseUSTCAdmission()
			if releaseSlot != nil {
				releaseSlot()
			}
		})
	}
}

func (r *AccountSelectionResult) USTCAdmissionRetryAfter() int {
	if r == nil || r.ustcRetryAt.IsZero() {
		return 1
	}
	return max(1, int(math.Ceil(time.Until(r.ustcRetryAt).Seconds())))
}

func ContextWithUSTCAdmission(ctx context.Context, selection *AccountSelectionResult) context.Context {
	if selection == nil || selection.ustcAdmission == nil {
		return ctx
	}
	return context.WithValue(ctx, ustcAdmissionContextKey{}, selection.ustcAdmission)
}

func (s *OpenAIGatewayService) reserveUSTC(ctx context.Context, account *Account) (*ustcAdmission, USTCCapacity, error) {
	capacity := USTCCapacity{State: "unknown"}
	limits, known := USTCAccountLimits(account)
	cache, configured := s.rpmCache.(USTCCapacityCache)
	if !known || !configured || USTCKeyScope(account) == "" {
		return nil, capacity, fmt.Errorf("USTC limits or shared capacity cache unavailable")
	}
	ticket, capacity, err := cache.USTCReserve(ctx, USTCKeyScope(account), limits)
	if reads, ok := ctx.Value(ustcCapacityReadsKey{}).(ustcCapacityReads); ok && err == nil {
		reads[USTCKeyScope(account)] = capacity
	}
	if err != nil || ticket == nil {
		return nil, capacity, err
	}
	origin := ctx
	if previous, _ := ctx.Value(ustcAdmissionContextKey{}).(*ustcAdmission); previous != nil && previous.origin != nil {
		origin = previous.origin
	}
	return &ustcAdmission{cache: cache, ticket: ticket, limits: limits, origin: origin, renewer: s.ustcLeaseRenewer(cache), notify: s.notifyUSTCCapacity}, capacity, nil
}

// The final gate also covers previous_response_id and experimental selections.
// No mandatory manual RPM setting and no sticky buffer applies to USTC.
func (s *OpenAIGatewayService) PrepareUSTCAdmission(ctx context.Context, selection *AccountSelectionResult) bool {
	if selection == nil || !isDefaultUSTCAccount(selection.Account) {
		return true
	}
	selection.ustcRetryAt = time.Now().Add(time.Second)
	account := s.ustcAccountForAdmission(ctx, selection.Account)
	if account == nil {
		return false
	}
	if userInfoQuotaSchedulingFailureReason(account, time.Now()) != "" {
		selection.ustcRetryAt = ustcQuotaNextCheck(account, time.Now())
		return false
	}
	selection.Account = account
	if selection.ustcAdmission != nil && selection.ustcAdmission.ticket.Scope == USTCKeyScope(account) {
		return true
	}
	if selection.ustcAdmission != nil {
		selection.ustcAdmission.release()
	}
	reservation, capacity, err := s.reserveUSTC(ctx, account)
	if err != nil || reservation == nil {
		if err == nil && !capacity.ResetAt.IsZero() && !ustcCapacityCanRecoverEarly(capacity) {
			selection.ustcRetryAt = capacity.ResetAt
		}
		return false
	}
	selection.ustcAdmission = reservation
	selection.ustcRetryAt = time.Time{}
	return true
}

func (s *OpenAIGatewayService) ustcAccountForAdmission(ctx context.Context, account *Account) *Account {
	ctx = context.WithValue(ctx, ustcQuotaBackgroundAdmissionKey{}, true)
	if refresher, ok := s.ustcQuotaRefresher.(USTCQuotaAdmissionRefresher); ok {
		refreshed, ready := refresher.QuotaForAdmission(ctx, account)
		if !ready {
			return nil
		}
		account = refreshed
	} else {
		account = s.refreshUSTCQuotaForScheduling(ctx, account)
	}
	if _, known := USTCAccountLimits(account); !known {
		return nil
	}
	// The persisted manual concurrency belongs to other providers. USTC derives
	// its admission limit from metadata, including accounts imported with old defaults.
	account = copyAccountForUserInfoQuotaRefresh(account)
	limits, _ := USTCAccountLimits(account)
	account.Concurrency = limits.Parallel
	return account
}

type ustcResponseBody struct {
	io.ReadCloser
	release func()
}

func (b *ustcResponseBody) Close() error { err := b.ReadCloser.Close(); b.release(); return err }

// All HTTP/SSE conversions use this transport gate. Internal retries get a new
// ticket too; streaming concurrency ends only when the response body is closed.
func (s *OpenAIGatewayService) doUSTCUpstream(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	ctx := request.Context()
	reservation, _ := ctx.Value(ustcAdmissionContextKey{}).(*ustcAdmission)
	if reservation != nil {
		reservation.mu.Lock()
		reusable := !reservation.committed && !reservation.released && reservation.ticket.Scope == USTCKeyScope(account)
		reservation.mu.Unlock()
		if !reusable {
			reservation.releaseWithNotification(true, true)
			reservation = nil
		}
	}
	if reservation == nil {
		// Retries and new WebSocket bridge turns may have no live selection
		// ticket. Refresh their metadata without blocking on an upstream probe,
		// and apply the same quota gate before creating a new reservation.
		current := s.ustcAccountForAdmission(ctx, account)
		if current == nil {
			return s.ustcLocalCapacityResponse(request, account), nil
		}
		account = current
		if userInfoQuotaSchedulingFailureReason(account, time.Now()) != "" {
			return s.ustcLocalCapacityResponse(request, account), nil
		}
		var err error
		reservation, _, err = s.reserveUSTC(ctx, account)
		if err != nil || reservation == nil {
			return s.ustcLocalCapacityResponse(request, account), nil
		}
	}
	// LiteLLM SSE omits remaining RPM. A cold Key gets one tiny counted
	// non-stream calibration rather than idling its entire first minute. After
	// a conservative cooldown the real queued request itself verifies recovery.
	if reservation.ticket.Cold {
		if model := ustcColdStreamingModel(request); model != "" {
			if err := s.calibrateUSTCStream(ctx, request, proxyURL, account, reservation, model); err != nil {
				return nil, err
			}
			var err error
			reservation, _, err = s.reserveUSTC(ctx, account)
			s.notifyUSTCCapacity()
			if err != nil || reservation == nil {
				return s.ustcLocalCapacityResponse(request, account), nil
			}
		}
	}
	if !reservation.commit(ctx) {
		reservation.release()
		return s.ustcLocalCapacityResponse(request, account), nil
	}
	resp, err := s.httpUpstream.Do(request, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		if reservation.ticket.Probe {
			observeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = reservation.cache.USTCObserve(observeCtx, reservation.ticket, USTCFeedback{RetryAfter: time.Minute})
			cancel()
		}
		reservation.release()
		return resp, err
	}
	var errorBody []byte
	if resp.StatusCode >= 400 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			_ = resp.Body.Close()
			reservation.release()
			return nil, readErr
		}
		// Preserve the normal error parser's body, including content beyond our bound.
		resp.Body = &ustcResponseBody{ReadCloser: &ustcPrefixedBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), closer: resp.Body}, release: reservation.release}
		errorBody = body
	} else {
		resp.Body = &ustcResponseBody{ReadCloser: resp.Body, release: reservation.release}
	}
	s.observeUSTCResponse(reservation, resp, errorBody)
	s.notifyUSTCCapacity()
	return resp, nil
}

func ustcColdStreamingModel(request *http.Request) string {
	if request.GetBody == nil || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return ""
	}
	body, err := request.GetBody()
	if err != nil {
		return ""
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil || !gjson.GetBytes(data, "stream").Bool() {
		return ""
	}
	return gjson.GetBytes(data, "model").String()
}

func (s *OpenAIGatewayService) calibrateUSTCStream(ctx context.Context, original *http.Request, proxyURL string, account *Account, reservation *ustcAdmission, model string) error {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "."}}, "max_tokens": 1, "stream": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, original.URL.String(), bytes.NewReader(body))
	if err != nil {
		reservation.release()
		return err
	}
	req.Header = original.Header.Clone()
	req.Header.Del("Content-Length")
	req.Header.Set("Accept", "application/json")
	if !reservation.commit(ctx) {
		reservation.release()
		return nil
	}
	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		observeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = reservation.cache.USTCObserve(observeCtx, reservation.ticket, USTCFeedback{RetryAfter: time.Minute})
		cancel()
		reservation.release()
		// The auxiliary request's error is not the real request's result. An
		// uncertain send conservatively closes admission; the next reserve sees it.
		return ctx.Err()
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		observeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = reservation.cache.USTCObserve(observeCtx, reservation.ticket, USTCFeedback{RetryAfter: time.Minute})
		cancel()
		reservation.release()
		return ctx.Err()
	}
	s.observeUSTCResponse(reservation, resp, data)
	// Obtain the real request's ticket before waking local pool waiters.
	reservation.releaseWithNotification(false, false)
	// Never send the calibration text to the client. If its feedback cannot
	// prove remaining capacity, the shared gate still blocks the real attempt.
	return nil
}

func (s *OpenAIGatewayService) observeUSTCResponse(reservation *ustcAdmission, resp *http.Response, body []byte) {
	feedback := USTCFeedback{StatusCode: resp.StatusCode, Remaining: ustcIntegerHeader(resp.Header, "x-ratelimit-api_key-remaining-requests"), RPMLimit: ustcIntegerHeader(resp.Header, "x-ratelimit-api_key-limit-requests")}
	feedback.Refund = resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(string(body)), "key_model_access_denied")
	if resp.StatusCode == http.StatusTooManyRequests {
		if isUSTCBudgetExhaustion429(body) {
			feedback.StatusCode = http.StatusInternalServerError
		} else {
			feedback.RetryAfter = ustc429Delay(resp.Header, time.Now())
		}
	} else if resp.StatusCode >= 400 && !feedback.Refund {
		feedback.RetryAfter = retryAfter(resp.Header, time.Now())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reservation.cache.USTCObserve(ctx, reservation.ticket, feedback); err != nil {
		slog.Warn("ustc_feedback_failed", "error", err)
	}
}

type ustcPrefixedBody struct {
	io.Reader
	closer io.Closer
}

func (b *ustcPrefixedBody) Close() error { return b.closer.Close() }

func ustcIntegerHeader(headers http.Header, key string) *int {
	value, err := strconv.Atoi(strings.TrimSpace(headers.Get(key)))
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

func (s *OpenAIGatewayService) ustcLocalCapacityResponse(request *http.Request, account *Account) *http.Response {
	retry := 1
	if userInfoQuotaSchedulingFailureReason(account, time.Now()) != "" {
		retry = max(retry, int(math.Ceil(time.Until(ustcQuotaNextCheck(account, time.Now())).Seconds())))
	}
	if limits, known := USTCAccountLimits(account); known {
		if cache, ok := s.rpmCache.(USTCCapacityCache); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			capacity, err := cache.USTCRead(ctx, USTCKeyScope(account), limits)
			cancel()
			if err == nil && !capacity.ResetAt.IsZero() && !ustcCapacityCanRecoverEarly(capacity) {
				retry = max(retry, int(math.Ceil(time.Until(capacity.ResetAt).Seconds())))
			}
		}
	}
	return &http.Response{StatusCode: 429, Status: "429 Too Many Requests", Request: request,
		Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{strconv.Itoa(retry)}, "X-Sub2api-Ustc-Local-Admission": []string{"1"}},
		Body:   io.NopCloser(strings.NewReader("{\"error\":{\"type\":\"rate_limit_error\",\"code\":\"gateway_account_limit\",\"message\":\"USTC capacity changed before forwarding; retry later\"}}"))}
}

// Rejection-time hints are conservative bounds, never proof of a new window.
func ustc429Delay(headers http.Header, now time.Time) time.Duration {
	delay := retryAfter(headers, now)
	if reset, err := parseUserInfoTime(headers.Get("reset_at")); err == nil {
		if serverNow, err := http.ParseTime(headers.Get("Date")); err == nil {
			delay = maxUSTCDuration(delay, reset.Sub(serverNow))
		} else {
			delay = maxUSTCDuration(delay, reset.Sub(now))
		}
	}
	if delay <= 0 {
		delay = time.Minute
	}
	return delay
}

func maxUSTCDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
