package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type ustcSpeedRunnerUpstream struct {
	HTTPUpstream
	mu           sync.Mutex
	calls        int
	requestBytes [][]byte
	response     []byte
	status       int
	header       http.Header
}

func ustcSpeedTimePtr(value time.Time) *time.Time { return &value }

func (u *ustcSpeedRunnerUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	requestBody, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.calls++
	u.requestBytes = append(u.requestBytes, append([]byte(nil), requestBody...))
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	responseBody := append([]byte(nil), u.response...)
	header := u.header.Clone()
	if header == nil {
		header = make(http.Header)
	}
	u.mu.Unlock()
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    req,
	}, nil
}

func (u *ustcSpeedRunnerUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *ustcSpeedRunnerUpstream) snapshot() (int, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	bodies := make([][]byte, len(u.requestBytes))
	for i, body := range u.requestBytes {
		bodies[i] = append([]byte(nil), body...)
	}
	return u.calls, bodies
}

type ustcSpeedRunnerRepository struct {
	USTCSpeedRepository
	mu             sync.Mutex
	runtime        *USTCSpeedRuntime
	acquireAllowed bool
	acquireErr     error
	acquireCalls   int
	releaseCalls   int
	inserted       []USTCSpeedSample
}

func (r *ustcSpeedRunnerRepository) GetRuntime(_ context.Context, scope string) (*USTCSpeedRuntime, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runtime == nil {
		return &USTCSpeedRuntime{KeyScope: scope}, nil
	}
	copy := *r.runtime
	copy.KeyScope = scope
	copy.Recent429 = append([]int64(nil), r.runtime.Recent429...)
	if r.runtime.NotBefore != nil {
		v := *r.runtime.NotBefore
		copy.NotBefore = &v
	}
	if r.runtime.Last429At != nil {
		v := *r.runtime.Last429At
		copy.Last429At = &v
	}
	return &copy, nil
}

func (r *ustcSpeedRunnerRepository) SaveRuntime(_ context.Context, scope string, runtime *USTCSpeedRuntime) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *runtime
	copy.KeyScope = scope
	copy.Recent429 = append([]int64(nil), runtime.Recent429...)
	r.runtime = &copy
	return nil
}

func (r *ustcSpeedRunnerRepository) AcquireKey(context.Context, string, string, time.Duration, int) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acquireCalls++
	return r.acquireAllowed, r.acquireErr
}

func (r *ustcSpeedRunnerRepository) ReleaseKey(context.Context, string, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	return nil
}

func (r *ustcSpeedRunnerRepository) InsertSample(_ context.Context, sample *USTCSpeedSample) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserted = append(r.inserted, *sample)
	return nil
}

func (r *ustcSpeedRunnerRepository) ListSamples(context.Context, USTCSpeedQuery) ([]USTCSpeedSample, int64, error) {
	return nil, 0, nil
}

func (r *ustcSpeedRunnerRepository) snapshot() (inserted []USTCSpeedSample, acquireCalls, releaseCalls int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]USTCSpeedSample(nil), r.inserted...), r.acquireCalls, r.releaseCalls
}

type ustcSpeedWaitCapacityCache struct {
	*ustcTestCapacityCache
	waiting      bool
	waitErr      error
	denyReserve  bool
	reserveCalls int
}

func (c *ustcSpeedWaitCapacityCache) USTCHasWaiting(context.Context) (bool, error) {
	return c.waiting, c.waitErr
}

func (c *ustcSpeedWaitCapacityCache) USTCReserve(ctx context.Context, scope string, limits USTCLimits) (*USTCTicket, USTCCapacity, error) {
	c.reserveCalls++
	if c.denyReserve {
		capacity, err := c.ustcTestCapacityCache.USTCRead(ctx, scope, limits)
		capacity.State = "sync_wait"
		return nil, capacity, err
	}
	return c.ustcTestCapacityCache.USTCReserve(ctx, scope, limits)
}

func newUSTCSpeedRunnerFixture() (*USTCSpeedService, *OpenAIGatewayService, *ustcSpeedRunnerRepository, *ustcTestCapacityCache, *ustcSpeedRunnerUpstream, Account) {
	gateway, cache := ustcSchedulerFixture(1, false)
	repo := &ustcSpeedRunnerRepository{acquireAllowed: true}
	speed := &USTCSpeedService{repo: repo, gateway: gateway, queue: make(chan USTCSpeedSample, 16)}
	settings := DefaultUSTCSpeedSettings()
	speed.cfg.Store(&settings)
	gateway.ustcSpeed = speed
	upstream := &ustcSpeedRunnerUpstream{response: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"), header: http.Header{"Content-Type": []string{"text/event-stream"}}}
	gateway.httpUpstream = upstream
	account := gateway.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	return speed, gateway, repo, cache, upstream, account
}

func TestUSTCSpeedObserverOnSharedTransportPreservesOriginalRequestAndResponse(t *testing.T) {
	speed, gateway, _, cache, upstream, account := newUSTCSpeedRunnerFixture()
	responseBytes := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"exact bytes\"}}]}\n\ndata: [DONE]\n\n")
	upstream.response = responseBytes
	originalRequestBytes := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"original input"}],"stream":true}`)
	selection, err := gateway.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	request, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewReader(originalRequestBytes))
	require.NoError(t, err)
	response, err := gateway.doUSTCUpstream(request, "", &account)
	require.NoError(t, err)
	gotBytes, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, responseBytes, gotBytes, "the observer must not consume or rewrite response bytes")
	require.NoError(t, response.Body.Close())

	calls, requestBodies := upstream.snapshot()
	require.Equal(t, 1, calls, "sampling must not issue an extra upstream HTTP request")
	require.Equal(t, [][]byte{originalRequestBytes}, requestBodies, "the forwarded request body must remain unchanged")
	capacity, err := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{RPM: 20, Parallel: 20})
	require.NoError(t, err)
	require.Equal(t, 1, capacity.Used, "sampling must consume only the original request's RPM")
	require.Zero(t, capacity.InFlight, "closing the original response releases its shared concurrency lease")

	select {
	case sample := <-speed.queue:
		require.Equal(t, "real", sample.Source)
		require.Equal(t, "success", sample.Status)
		require.Equal(t, "deepseek-flash", sample.Model)
		require.Equal(t, len("exact bytes"), sample.OutputChars)
		require.True(t, sample.StreamDone)
	case <-time.After(time.Second):
		t.Fatal("doUSTCUpstream did not publish the observed sample")
	}
}

func TestUSTCSpeedObserverCloseHonorsProtocolTerminalFrame(t *testing.T) {
	for _, tc := range []struct {
		name       string
		terminal   bool
		wantStatus string
		wantReason string
	}{
		{name: "terminal frame before close", terminal: true, wantStatus: "success"},
		{name: "early close without terminal", terminal: false, wantStatus: "error", wantReason: "stream_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			speed, gateway, _, _, upstream, account := newUSTCSpeedRunnerFixture()
			responseBytes := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n")
			if tc.terminal {
				responseBytes = append(responseBytes, []byte("data: [DONE]\n\n")...)
			}
			upstream.response = responseBytes
			selection, err := gateway.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
			require.NoError(t, err)
			defer selection.ReleaseFunc()
			request, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewReader([]byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"input"}],"stream":true}`)))
			require.NoError(t, err)
			response, err := gateway.doUSTCUpstream(request, "", &account)
			require.NoError(t, err)

			// Read exactly the bytes through the terminal frame, without a further
			// Read that could return io.EOF, then close as a normal HTTP consumer may.
			buffer := make([]byte, len(responseBytes))
			n, readErr := response.Body.Read(buffer)
			require.NoError(t, readErr)
			require.Equal(t, len(responseBytes), n)
			require.Equal(t, responseBytes, buffer)
			require.NoError(t, response.Body.Close())
			select {
			case sample := <-speed.queue:
				require.Equal(t, tc.wantStatus, sample.Status)
				require.Equal(t, tc.wantReason, sample.Reason)
				require.Equal(t, tc.terminal, sample.StreamDone)
			case <-time.After(time.Second):
				t.Fatal("closing the response did not finish the observer")
			}
		})
	}
}

func TestUSTCSpeedObserverNonStreamingContentWithoutUsageIsSuccess(t *testing.T) {
	speed, gateway, _, _, upstream, account := newUSTCSpeedRunnerFixture()
	responseBytes := []byte(`{"choices":[{"message":{"content":"answer"}}]}`)
	upstream.response = responseBytes
	upstream.header = http.Header{"Content-Type": []string{"application/json"}}
	// An invalid, user-controlled effort value must not be copied into the
	// persisted profile or make the profile unbounded.
	untrustedEffort := strings.Repeat("untrusted-effort-value-", 1024)
	requestBody := []byte(`{"model":"deepseek-flash","messages":[{"role":"user","content":"input"}],"stream":false,"reasoning_effort":"` + untrustedEffort + `"}`)
	selection, err := gateway.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	request, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/chat/completions", bytes.NewReader(requestBody))
	require.NoError(t, err)
	response, err := gateway.doUSTCUpstream(request, "", &account)
	require.NoError(t, err)
	gotBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, responseBytes, gotBody)
	require.NoError(t, response.Body.Close())

	select {
	case sample := <-speed.queue:
		require.Equal(t, "success", sample.Status)
		require.Equal(t, len("answer"), sample.OutputChars)
		require.False(t, sample.InputTokensKnown)
		require.False(t, sample.TokenCountKnown)
		require.Nil(t, sample.FirstOutputMS)
		require.Nil(t, sample.TTFTMS)
		require.Nil(t, sample.OutputMS)
		require.Nil(t, sample.TailMS)
		require.Nil(t, sample.GenerationTPS)
		require.Nil(t, sample.OverallTPS)
		require.Contains(t, sample.Profile, ":unknown:in=unknown:out=unknown:")
		require.NotContains(t, sample.Profile, untrustedEffort)
		require.Less(t, len(sample.Profile), 256)
	case <-time.After(time.Second):
		t.Fatal("non-stream response did not produce an observed sample")
	}
}

func TestUSTCSpeedObserverResponseFailedWithoutErrorCodeIsError(t *testing.T) {
	speed, gateway, _, _, upstream, account := newUSTCSpeedRunnerFixture()
	responseBytes := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n")
	upstream.response = responseBytes
	selection, err := gateway.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	defer selection.ReleaseFunc()
	request, err := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), http.MethodPost, "https://api.llm.ustc.edu.cn/v1/responses", bytes.NewReader([]byte(`{"model":"deepseek-flash","input":"input","stream":true}`)))
	require.NoError(t, err)
	response, err := gateway.doUSTCUpstream(request, "", &account)
	require.NoError(t, err)
	_, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	select {
	case sample := <-speed.queue:
		require.Equal(t, "error", sample.Status)
		require.Equal(t, "upstream_stream_error", sample.Reason)
		require.Equal(t, "response_failed", sample.ErrorCode)
		require.True(t, sample.StreamDone)
	case <-time.After(time.Second):
		t.Fatal("failed Responses stream did not produce an observed sample")
	}
}

func TestUSTCSpeedManualProbeUsesSharedAdmissionAndOneRequest(t *testing.T) {
	speed, _, repo, cache, upstream, account := newUSTCSpeedRunnerFixture()
	task := DefaultUSTCSpeedTask()
	task.ID = 71
	task.RealFallback = false
	status := speed.runProbe(context.Background(), task, &account, "deepseek-flash", "synthetic prompt", "round-1", time.Now())
	require.Equal(t, "success", status)
	calls, bodies := upstream.snapshot()
	require.Equal(t, 1, calls, "a warm SSE probe should use one shared-admission request")
	require.Len(t, bodies, 1)
	require.Equal(t, 1, cache.used[USTCKeyScope(&account)], "the real probe should commit exactly one RPM ticket")
	inserted, acquireCalls, releaseCalls := repo.snapshot()
	require.Len(t, inserted, 1)
	require.Equal(t, "probe", inserted[0].Source)
	require.Equal(t, "success", inserted[0].Status)
	require.Equal(t, 1, acquireCalls)
	require.Equal(t, 1, releaseCalls)
}

func TestUSTCSpeedManualProbeRefusesUnsafeConditions(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*OpenAIGatewayService, *ustcSpeedRunnerRepository, *ustcTestCapacityCache, Account)
		wantReason  string
		wantAcquire bool
	}{
		{
			name: "user queue",
			setup: func(gateway *OpenAIGatewayService, _ *ustcSpeedRunnerRepository, base *ustcTestCapacityCache, _ Account) {
				gateway.rpmCache = &ustcSpeedWaitCapacityCache{ustcTestCapacityCache: base, waiting: true}
			},
			wantReason: "user_load_or_capacity",
		},
		{
			name: "low rpm headroom",
			setup: func(_ *OpenAIGatewayService, _ *ustcSpeedRunnerRepository, base *ustcTestCapacityCache, account Account) {
				base.used[USTCKeyScope(&account)] = 16 // 4 remaining is below reserveRPM+the probe itself.
			},
			wantReason: "user_load_or_capacity",
		},
		{
			name: "shared capacity cooldown",
			setup: func(gateway *OpenAIGatewayService, _ *ustcSpeedRunnerRepository, base *ustcTestCapacityCache, _ Account) {
				gateway.rpmCache = &ustcSpeedWaitCapacityCache{ustcTestCapacityCache: base, denyReserve: true}
			},
			wantReason:  "shared_admission",
			wantAcquire: true,
		},
		{
			name: "global physical key lease held",
			setup: func(_ *OpenAIGatewayService, repo *ustcSpeedRunnerRepository, _ *ustcTestCapacityCache, _ Account) {
				repo.acquireAllowed = false
			},
			wantReason:  "background_capacity",
			wantAcquire: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			speed, gateway, repo, cache, upstream, account := newUSTCSpeedRunnerFixture()
			tc.setup(gateway, repo, cache, account)
			task := DefaultUSTCSpeedTask()
			task.ID = 72
			task.RealFallback = false
			status := speed.runProbe(context.Background(), task, &account, "deepseek-flash", "synthetic prompt", "round-2", time.Now())
			require.Equal(t, "skipped", status)
			calls, _ := upstream.snapshot()
			require.Zero(t, calls, "unsafe or contended admission must not send a probe request")
			inserted, acquireCalls, releaseCalls := repo.snapshot()
			require.Len(t, inserted, 1)
			require.Equal(t, "skipped", inserted[0].Status)
			require.Equal(t, tc.wantReason, inserted[0].Reason)
			if tc.wantAcquire {
				require.Equal(t, 1, acquireCalls)
			} else {
				require.Zero(t, acquireCalls)
			}
			capacity, err := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{RPM: 20, Parallel: 20})
			require.NoError(t, err)
			require.Zero(t, capacity.Pending, "a skipped probe must release any shared pending ticket")
			if tc.name == "shared capacity cooldown" {
				require.Equal(t, 1, releaseCalls, "shared admission rejection still releases the global Key lease")
			}
		})
	}
}

func TestUSTCSpeedBackoffWindowDeadlineAndRecovery(t *testing.T) {
	task := DefaultUSTCSpeedTask()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	last429 := now.Add(-2 * time.Minute)
	runtime := &USTCSpeedRuntime{
		Recent429: []int64{
			now.Add(-20 * time.Minute).UnixMilli(), // outside the observation window
			now.Add(-4 * time.Minute).UnixMilli(),
			now.Add(-3 * time.Minute).UnixMilli(),
			last429.UnixMilli(),
		},
		Last429At: &last429,
	}
	blocked, reason := speedBackoff(task, runtime, now)
	require.True(t, blocked)
	require.Equal(t, "429_backoff", reason)
	require.NotNil(t, runtime.NotBefore)
	wantDeadline := last429.Add(time.Duration(task.IntervalSeconds*2) * time.Second)
	require.Equal(t, wantDeadline, *runtime.NotBefore)

	deadline := *runtime.NotBefore
	blocked, reason = speedBackoff(task, runtime, now.Add(5*time.Minute))
	require.True(t, blocked)
	require.Equal(t, "429_backoff", reason)
	require.Equal(t, deadline, *runtime.NotBefore, "a repeated skip must not extend the existing backoff deadline")

	blocked, reason = speedBackoff(task, runtime, deadline.Add(time.Second))
	require.False(t, blocked, "expiry allows the shared-gate half-open attempt")
	require.Empty(t, reason)

	paused := &USTCSpeedRuntime{Paused: true, NotBefore: ustcSpeedTimePtr(now.Add(time.Hour)), SuccessStreak: task.RecoverySuccesses}
	blocked, reason = speedBackoff(task, paused, now)
	require.False(t, blocked, "the configured consecutive successes recover the Key")
	require.Empty(t, reason)
	require.False(t, paused.Paused)
	require.Nil(t, paused.NotBefore)
}

func TestUSTCSpeedBackoffPauseExpiryStillRequiresGlobalLease(t *testing.T) {
	speed, _, repo, _, upstream, account := newUSTCSpeedRunnerFixture()
	task := DefaultUSTCSpeedTask()
	task.ID = 73
	task.RealFallback = false
	task.PauseAfter429 = task.BackoffAfter429
	now := time.Now()
	last429 := now.Add(-3 * time.Hour)
	recent := make([]int64, task.PauseAfter429)
	for i := range recent {
		recent[i] = last429.Add(-time.Duration(i) * time.Minute).UnixMilli()
	}
	repo.runtime = &USTCSpeedRuntime{Recent429: recent, Last429At: &last429, Paused: true, NotBefore: ustcSpeedTimePtr(now.Add(-time.Minute))}
	// The local backoff deadline expired, but the shared Key lease is held by
	// another probe. The half-open request must stop at the global lease gate.
	repo.acquireAllowed = false
	status := speed.runProbe(context.Background(), task, &account, "deepseek-flash", "synthetic prompt", "round-3", now)
	require.Equal(t, "skipped", status)
	calls, _ := upstream.snapshot()
	require.Zero(t, calls)
	inserted, acquireCalls, _ := repo.snapshot()
	require.Equal(t, 1, acquireCalls, "expired backoff proceeds to the shared global lease gate")
	require.Len(t, inserted, 1)
	require.Equal(t, "background_capacity", inserted[0].Reason)
}

func TestUSTCSpeedActiveWindowUsesTimezoneAndSupportsCrossMidnight(t *testing.T) {
	task := DefaultUSTCSpeedTask()
	task.Timezone = "Asia/Shanghai"
	task.ActiveStart = "22:00"
	task.ActiveEnd = "02:00"

	for _, tc := range []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "before local start", now: time.Date(2026, 10, 26, 13, 59, 0, 0, time.UTC), want: false}, // 21:59 Shanghai
		{name: "at local start", now: time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC), want: true},       // 22:00 Shanghai
		{name: "after local midnight", now: time.Date(2026, 10, 26, 17, 0, 0, 0, time.UTC), want: true}, // 01:00 Shanghai
		{name: "after local end", now: time.Date(2026, 10, 26, 18, 1, 0, 0, time.UTC), want: false},     // 02:01 Shanghai
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, speedTaskActive(task, tc.now))
		})
	}

	// The weekday is Tuesday in the configured location while the UTC date is
	// still Monday, so evaluating weekdays in UTC would reject this time.
	task.ActiveStart, task.ActiveEnd = "00:00", "23:59"
	task.DaysOfWeek = []int{int(time.Tuesday)}
	tuesdayAt00Shanghai := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC)
	require.True(t, speedTaskActive(task, tuesdayAt00Shanghai))
}

func TestUSTCSpeedBackoffPausedWithoutDeadlineBlocks(t *testing.T) {
	task := DefaultUSTCSpeedTask()
	runtime := &USTCSpeedRuntime{Paused: true}
	blocked, reason := speedBackoff(task, runtime, time.Now())
	require.True(t, blocked, "a pause without a deadline must not silently probe")
	require.Equal(t, "429_paused", reason)
}

// ustcSpeedAccountsRepo supplies the full-account listing used by scheduled
// tasks; the scheduler fixture only implements its own scheduling queries.
type ustcSpeedAccountsRepo struct {
	schedulerTestOpenAIAccountRepo
}

func (r ustcSpeedAccountsRepo) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func TestUSTCSpeedManualRunBypassesActiveWindow(t *testing.T) {
	speed, gateway, _, _, upstream, _ := newUSTCSpeedRunnerFixture()
	accounts := gateway.accountRepo.(schedulerTestOpenAIAccountRepo)
	speed.accounts = ustcSpeedAccountsRepo{accounts}
	task := DefaultUSTCSpeedTask()
	task.Models = []string{"deepseek-flash"}
	task.AllAccounts = true
	task.RealFallback = false
	task.MaxParallelAccounts = 1

	location, err := time.LoadLocation(task.Timezone)
	require.NoError(t, err)
	now := time.Now().In(location)
	start := (now.Hour()*60 + now.Minute() + 5) % (24 * 60)
	task.ActiveStart = fmt.Sprintf("%02d:%02d", start/60, start%60)
	end := (start + 1) % (24 * 60)
	task.ActiveEnd = fmt.Sprintf("%02d:%02d", end/60, end%60)
	require.NotEqual(t, task.ActiveStart, task.ActiveEnd)

	require.Equal(t, "outside_active_window", speed.executeTask(context.Background(), task, "scheduled", false))
	calls, _ := upstream.snapshot()
	require.Zero(t, calls, "a scheduled run outside the window must not spend upstream quota")

	status := speed.executeTask(context.Background(), task, "manual", true)
	require.NotEqual(t, "outside_active_window", status)
	calls, _ = upstream.snapshot()
	require.Equal(t, 1, calls, "a manual run should proceed despite the active window")
}

func TestUSTCSpeedProbeBodyAndEndpoint(t *testing.T) {
	topP := 0.7
	seed := int64(123)
	task := DefaultUSTCSpeedTask()
	task.APIMode = "chat_completions"
	task.Stream = true
	task.MaxTokens = 77
	task.ThinkingMode = "on"
	task.ReasoningEffort = "high"
	task.TopP = &topP
	task.Seed = &seed
	task.Stop = []string{"END"}

	body, err := speedProbeBody(task, "mapped-model", "hello probe")
	require.NoError(t, err)
	var chat map[string]any
	require.NoError(t, json.Unmarshal(body, &chat))
	require.Equal(t, "mapped-model", chat["model"])
	require.Equal(t, true, chat["stream"])
	require.Equal(t, float64(77), chat["max_tokens"])
	require.Equal(t, map[string]any{"include_usage": true}, chat["stream_options"])
	require.Equal(t, map[string]any{"type": "enabled"}, chat["thinking"])
	require.Equal(t, "high", chat["reasoning_effort"])
	require.Equal(t, []any{"END"}, chat["stop"])
	require.Equal(t, float64(0.7), chat["top_p"])
	require.Equal(t, float64(123), chat["seed"])
	require.Equal(t, "hello probe", chat["messages"].([]any)[0].(map[string]any)["content"])

	task.APIMode = "responses"
	task.ThinkingMode = "off"
	body, err = speedProbeBody(task, "mapped-model", "response input")
	require.NoError(t, err)
	var responses map[string]any
	require.NoError(t, json.Unmarshal(body, &responses))
	require.Equal(t, "response input", responses["input"])
	require.Equal(t, float64(77), responses["max_output_tokens"])
	require.Equal(t, map[string]any{"effort": "none"}, responses["reasoning"])
	require.NotContains(t, responses, "messages")
	require.NotContains(t, responses, "stream_options")

	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn"}}
	endpoint, err := speedProbeURL(account, "chat_completions")
	require.NoError(t, err)
	require.Equal(t, "https://api.llm.ustc.edu.cn/v1/chat/completions", endpoint)
	account.Credentials["base_url"] = "https://api.llm.ustc.edu.cn/custom/v1/"
	endpoint, err = speedProbeURL(account, "responses")
	require.NoError(t, err)
	require.Equal(t, "https://api.llm.ustc.edu.cn/custom/v1/responses", endpoint)
	for _, invalidBase := range []string{
		"http://api.llm.ustc.edu.cn",
		"https://api.llm.ustc.edu.cn.evil.example",
		"https://user:pass@api.llm.ustc.edu.cn",
		"https://api.llm.ustc.edu.cn?override=1",
		"https://api.llm.ustc.edu.cn/#fragment",
	} {
		account.Credentials["base_url"] = invalidBase
		_, err := speedProbeURL(account, "chat_completions")
		require.Error(t, err, invalidBase)
	}
}

func TestValidateUSTCSpeedTaskAndCronScheduling(t *testing.T) {
	task := DefaultUSTCSpeedTask()
	task.Name = "  valid task  "
	task.Models = []string{" model-a ", "model-a", "model-b"}
	task.AccountIDs = []int64{9}
	task.CronExpression = "0 9 * * *"
	task.JitterSeconds = 0
	require.NoError(t, ValidateUSTCSpeedTask(&task))
	require.Equal(t, "valid task", task.Name)
	require.Equal(t, []string{"model-a", "model-b"}, task.Models)

	location, err := time.LoadLocation(task.Timezone)
	require.NoError(t, err)
	now := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	next := nextUSTCSpeedTask(task, now)
	require.Equal(t, time.Date(2026, 10, 26, 9, 0, 0, 0, location), next)

	invalidTasks := []func(*USTCSpeedTask){
		func(task *USTCSpeedTask) { task.Models = []string{"\n"} },
		func(task *USTCSpeedTask) { task.CronExpression = "TZ=UTC */5 * * * *" },
		func(task *USTCSpeedTask) { task.Timezone = "No/Such_Zone" },
		func(task *USTCSpeedTask) { task.ActiveStart = "24:00" },
		func(task *USTCSpeedTask) { task.DaysOfWeek = []int{7} },
		func(task *USTCSpeedTask) { task.APIMode = "unknown" },
		func(task *USTCSpeedTask) { task.MaxTokens = 15 },
	}
	for i, mutate := range invalidTasks {
		t.Run(fmt.Sprintf("invalid_%d", i), func(t *testing.T) {
			invalid := task
			invalid.Models = append([]string(nil), task.Models...)
			invalid.AccountIDs = append([]int64(nil), task.AccountIDs...)
			invalid.DaysOfWeek = append([]int(nil), task.DaysOfWeek...)
			mutate(&invalid)
			require.Error(t, ValidateUSTCSpeedTask(&invalid))
		})
	}
}
