package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ustcCalibrationUpstream struct {
	HTTPUpstream
	bodies []map[string]any
	auth   []string
	status int
}

func (u *ustcCalibrationUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	var body map[string]any
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	u.auth = append(u.auth, req.Header.Get("Authorization"))
	if body["stream"] == true {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: real-answer\n\ndata: [DONE]\n\n")), Request: req}, nil
	}
	status := u.status
	if status == 0 {
		status = 200
	}
	headers := http.Header{"X-Ratelimit-Api_key-Limit-Requests": []string{"20"}, "X-Ratelimit-Api_key-Remaining-Requests": []string{"19"}}
	if status == http.StatusTooManyRequests {
		headers.Set("Retry-After", "60")
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader("calibration-answer")), Request: req}, nil
}

type ustcCalibrationCapacityCache struct{ *ustcTestCapacityCache }

func (c ustcCalibrationCapacityCache) USTCObserve(ctx context.Context, ticket *USTCTicket, feedback USTCFeedback) error {
	if feedback.RetryAfter > 0 {
		return c.USTCCooldown(ctx, ticket.Scope, feedback.RetryAfter)
	}
	return nil
}

func TestUSTCOrdinaryCalibrationFailureStillForwardsRealRequest(t *testing.T) {
	for _, status := range []int{400, 401, 403, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			svc, cache := ustcSchedulerFixture(1, false)
			svc.rpmCache = ustcCalibrationCapacityCache{cache}
			upstream := &ustcCalibrationUpstream{status: status}
			svc.httpUpstream = upstream
			selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
			require.NoError(t, err)
			defer selection.ReleaseFunc()
			selection.ustcAdmission.ticket.Probe, selection.ustcAdmission.ticket.Cold = true, true
			req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			require.NoError(t, err)
			resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			data, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Contains(t, string(data), "real-answer")
			require.NotContains(t, string(data), "calibration-answer")
			require.Len(t, upstream.bodies, 2)
			require.NoError(t, resp.Body.Close())
			capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
			require.Equal(t, "ready", capacity.State)
			require.Equal(t, 2, capacity.Used)
			require.Zero(t, capacity.InFlight)
		})
	}
}

func TestUSTCColdSSECalibrationIsCountedAndNeverForwarded(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcCalibrationUpstream{}
	svc.httpUpstream = upstream
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	selection.ustcAdmission.ticket.Probe = true
	selection.ustcAdmission.ticket.Cold = true
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":100}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer synthetic-test-key")
	resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(data), "real-answer")
	require.NotContains(t, string(data), "calibration-answer")
	require.Len(t, upstream.bodies, 2)
	require.Equal(t, "deepseek-flash", upstream.bodies[0]["model"])
	require.Equal(t, false, upstream.bodies[0]["stream"])
	require.Equal(t, float64(1), upstream.bodies[0]["max_tokens"])
	require.Equal(t, true, upstream.bodies[1]["stream"])
	require.Equal(t, float64(100), upstream.bodies[1]["max_tokens"])
	require.Equal(t, []string{"Bearer synthetic-test-key", "Bearer synthetic-test-key"}, upstream.auth)
	capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Equal(t, 2, capacity.Used, "calibration and real forwarding each consume one RPM")
	require.Equal(t, 1, capacity.InFlight, "only the real stream retains a lease")
	require.NoError(t, resp.Body.Close())
	capacity, _ = cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Zero(t, capacity.InFlight)
}

func TestUSTCRecoverySSEUsesOnlyRealRequest(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcCalibrationUpstream{}
	svc.httpUpstream = upstream
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	selection.ustcAdmission.ticket.Probe = true
	selection.ustcAdmission.ticket.Cold = false
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	require.NoError(t, err)
	resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Len(t, upstream.bodies, 1, "a conservative cooldown needs no extra health request")
	require.Equal(t, true, upstream.bodies[0]["stream"])
	capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Equal(t, 1, capacity.Used)
	require.Zero(t, capacity.InFlight)
}

func TestUSTCRejectedColdCalibrationDoesNotSendRealRequest(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	svc.rpmCache = ustcCalibrationCapacityCache{cache}
	upstream := &ustcCalibrationUpstream{status: http.StatusTooManyRequests}
	svc.httpUpstream = upstream
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	selection.ustcAdmission.ticket.Probe = true
	selection.ustcAdmission.ticket.Cold = true
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewBufferString(`{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	require.NoError(t, err)
	resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.Equal(t, "1", resp.Header.Get("X-Sub2api-Ustc-Local-Admission"))
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(data), "calibration-answer")
	require.NoError(t, resp.Body.Close())
	require.Len(t, upstream.bodies, 1)
	capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Equal(t, 1, capacity.Used)
	require.Zero(t, capacity.InFlight)
}

func TestUSTCLocalCapacityRetryAfterIgnoresAlreadyCommittedLongStream(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	reservation, _, err := svc.reserveUSTC(context.Background(), &account)
	require.NoError(t, err)
	require.True(t, reservation.commit(context.Background()))
	defer reservation.release()
	cache.mu.Lock()
	cache.used[USTCKeyScope(&account)] = 20
	cache.mu.Unlock()
	request, err := http.NewRequest(http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	require.NoError(t, err)
	response := svc.ustcLocalCapacityResponse(request, &account)
	defer response.Body.Close()
	require.Equal(t, "60", response.Header.Get("Retry-After"))
}

func TestUSTCTransportReleasesMismatchedPendingTicketBeforeReserving(t *testing.T) {
	svc, cache := ustcSchedulerFixture(2, false)
	accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
	reservation, _, err := svc.reserveUSTC(context.Background(), &accounts[0])
	require.NoError(t, err)
	selection := &AccountSelectionResult{Account: &accounts[0], ustcAdmission: reservation}
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	require.NoError(t, err)
	svc.httpUpstream = &ustcTransportStub{}
	resp, err := svc.doOpenAIUpstream(req, "", &accounts[1])
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	old, err := cache.USTCRead(context.Background(), USTCKeyScope(&accounts[0]), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Zero(t, old.Pending)
	require.Zero(t, old.InFlight)
	require.Zero(t, old.Used)
	current, err := cache.USTCRead(context.Background(), USTCKeyScope(&accounts[1]), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Equal(t, 1, current.Used)
	require.Zero(t, current.InFlight)
}

type ustcRecoveryCapacityCache struct{ *ustcTestCapacityCache }

func (c ustcRecoveryCapacityCache) USTCRead(ctx context.Context, scope string, limits USTCLimits) (USTCCapacity, error) {
	value, err := c.ustcTestCapacityCache.USTCRead(ctx, scope, limits)
	value.State = "verify_one"
	value.Available = 1
	return value, err
}

func TestUSTCRecoverySelectionDoesNotWaitForPreviousWindowStream(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	scope := USTCKeyScope(&account)
	oldStream, _, err := cache.USTCReserve(context.Background(), scope, USTCLimits{20, 20})
	require.NoError(t, err)
	committed, err := cache.USTCCommit(context.Background(), oldStream, USTCLimits{20, 20})
	require.NoError(t, err)
	require.True(t, committed)
	defer cache.USTCRelease(context.Background(), oldStream)
	svc.rpmCache = ustcRecoveryCapacityCache{cache}
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err, "available parallel capacity admits a single recovery request beside an old stream")
	selection.ReleaseFunc()
}

func TestUSTCClientDisconnectKeepsCommittedUpstreamLeaseUntilBodyClose(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcTransportStub{}
	svc.httpUpstream = upstream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.WithoutCancel(ctx), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	require.NoError(t, err)
	resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
	require.NoError(t, err)
	cancel()
	selection.ReleaseFunc()
	capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Equal(t, 1, capacity.InFlight, "detached forwarding still occupies upstream concurrency")
	require.Equal(t, 1, capacity.Used)
	require.NoError(t, resp.Body.Close())
	capacity, _ = cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Zero(t, capacity.InFlight)
}

func TestUSTCCancellationBeforeDetachedSendRefundsReservation(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcTransportStub{}
	svc.httpUpstream = upstream
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	req, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.WithoutCancel(ctx), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	require.NoError(t, err)
	cancel()
	resp, err := svc.doOpenAIUpstream(req, "", selection.Account)
	require.NoError(t, err)
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Zero(t, upstream.calls)
	capacity, _ := cache.USTCRead(context.Background(), USTCKeyScope(selection.Account), USTCLimits{20, 20})
	require.Zero(t, capacity.Used)
	require.Zero(t, capacity.Pending)
	require.Zero(t, capacity.InFlight)
}

func TestUSTC429DelayUsesConservativeHintsAndServerClock(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	serverNow := now.Add(-10 * time.Minute)
	for _, tc := range []struct {
		name string
		http.Header
		delay time.Duration
	}{
		{"missing", http.Header{}, time.Minute},
		{"invalid", http.Header{"Retry-After": []string{"bogus"}, "Reset_at": []string{"bogus"}}, time.Minute},
		{"retry", http.Header{"Retry-After": []string{"75"}}, 75 * time.Second},
		{"clock-skew", http.Header{"Retry-After": []string{"60"}, "Date": []string{serverNow.Format(http.TimeFormat)}, "Reset_at": []string{serverNow.Add(90 * time.Second).Format(time.RFC3339)}}, 90 * time.Second},
		{"later-retry", http.Header{"Retry-After": []string{"120"}, "Date": []string{serverNow.Format(http.TimeFormat)}, "Reset_at": []string{serverNow.Add(90 * time.Second).Format(time.RFC3339)}}, 120 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.delay, ustc429Delay(tc.Header, now)) })
	}
}

type ustcFeedbackCapture struct {
	*ustcTestCapacityCache
	feedback USTCFeedback
}

func (c *ustcFeedbackCapture) USTCObserve(_ context.Context, _ *USTCTicket, feedback USTCFeedback) error {
	c.feedback = feedback
	return nil
}

func TestUSTCFeedbackSeparatesRPMBudgetAndPreAdmissionDenial(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		status, rated int
		refund        bool
		delay         time.Duration
	}{
		{"rpm", `{"error":"rate limit requests exceeded"}`, 429, 429, false, 60 * time.Second},
		{"budget", `{"error":"budget exceeded"}`, 429, 500, false, 0},
		{"model-permission", `{"error":{"code":"key_model_access_denied"}}`, 403, 403, true, 0},
		{"other-permission", `{"error":"forbidden"}`, 403, 403, false, 0},
		{"bad-request", `{"error":"messages is empty"}`, 400, 400, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, original := ustcSchedulerFixture(1, false)
			cache := &ustcFeedbackCapture{ustcTestCapacityCache: original}
			reservation := &ustcAdmission{cache: cache, ticket: &USTCTicket{Scope: "synthetic-scope", ID: "synthetic-ticket", Epoch: 1}}
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			svc.observeUSTCResponse(reservation, resp, []byte(tc.body))
			require.Equal(t, tc.rated, cache.feedback.StatusCode)
			require.Equal(t, tc.refund, cache.feedback.Refund)
			require.Equal(t, tc.delay, cache.feedback.RetryAfter)
		})
	}
}
