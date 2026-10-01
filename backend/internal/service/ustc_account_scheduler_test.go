package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Routing tests use a controlled store. Redis tests exercise the real atomic
// window/recovery implementation, avoiding a second copy of that state machine.
type ustcTestCapacityCache struct {
	RPMCache
	mu      sync.Mutex
	used    map[string]int
	tickets map[string]*ustcTestTicket
	serial  int
	blocked map[string]time.Time
}
type ustcTestTicket struct {
	scope string
	sent  bool
}

func (c *ustcTestCapacityCache) capacity(scope string, limits USTCLimits) USTCCapacity {
	inFlight, pending := 0, 0
	for _, ticket := range c.tickets {
		if ticket.scope == scope {
			inFlight++
			if !ticket.sent {
				pending++
			}
		}
	}
	state := "ready"
	if time.Now().Before(c.blocked[scope]) {
		state = "sync_wait"
	}
	reset := time.Now().Add(time.Minute)
	if state == "sync_wait" {
		reset = c.blocked[scope]
	}
	return USTCCapacity{State: state, RPMLimit: &limits.RPM, ParallelLimit: &limits.Parallel, Used: c.used[scope], Pending: pending, InFlight: inFlight, Available: max(0, min(limits.RPM-c.used[scope]-pending, limits.Parallel-inFlight)), ResetAt: reset}
}
func (c *ustcTestCapacityCache) USTCRead(_ context.Context, scope string, limits USTCLimits) (USTCCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity(scope, limits), nil
}
func (c *ustcTestCapacityCache) USTCReserve(_ context.Context, scope string, limits USTCLimits) (*USTCTicket, USTCCapacity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value := c.capacity(scope, limits)
	if value.Available <= 0 || value.State == "sync_wait" {
		return nil, value, nil
	}
	c.serial++
	id := fmt.Sprint(c.serial)
	c.tickets[id] = &ustcTestTicket{scope: scope}
	return &USTCTicket{Scope: scope, ID: id, Epoch: 1}, c.capacity(scope, limits), nil
}
func (c *ustcTestCapacityCache) USTCCommit(_ context.Context, ticket *USTCTicket, _ USTCLimits) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.tickets[ticket.ID]
	if entry == nil || entry.sent || time.Now().Before(c.blocked[ticket.Scope]) {
		return false, nil
	}
	entry.sent = true
	c.used[ticket.Scope]++
	return true, nil
}
func (c *ustcTestCapacityCache) USTCRelease(_ context.Context, ticket *USTCTicket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.tickets, ticket.ID)
	return nil
}
func (c *ustcTestCapacityCache) USTCRenew(context.Context, *USTCTicket) error { return nil }
func (c *ustcTestCapacityCache) USTCObserve(context.Context, *USTCTicket, USTCFeedback) error {
	return nil
}
func (c *ustcTestCapacityCache) USTCCooldown(_ context.Context, scope string, delay time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocked[scope] = time.Now().Add(delay)
	return nil
}

type ustcTestStickyCache struct {
	schedulerTestGatewayCache
	mu sync.Mutex
}

func (c *ustcTestStickyCache) GetSessionAccountID(ctx context.Context, group int64, hash string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, group, hash)
}
func (c *ustcTestStickyCache) SetSessionAccountID(ctx context.Context, group int64, hash string, id int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.schedulerTestGatewayCache.SetSessionAccountID(ctx, group, hash, id, ttl)
}

func ustcSchedulerFixture(n int, batch bool) (*OpenAIGatewayService, *ustcTestCapacityCache) {
	accounts := make([]Account, n)
	for i := range accounts {
		lastUsed := time.Now().Add(time.Duration(i) * -time.Hour)
		accounts[i] = Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 20, LastUsedAt: &lastUsed,
			Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": fmt.Sprintf("test-key-%d", i)},
			Extra:       map[string]any{"base_rpm": 20, "rpm_strategy": "sticky_exempt", "rpm_sticky_buffer": 5, "upstream_userinfo_limits_known": true, "upstream_userinfo_rpm_limit": 20, "upstream_userinfo_max_parallel_requests": 20},
		}
	}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = batch
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
	cache := &ustcTestCapacityCache{used: make(map[string]int), tickets: make(map[string]*ustcTestTicket), blocked: make(map[string]time.Time)}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &ustcTestStickyCache{}, cfg: cfg, rpmCache: cache}
	return svc, cache
}

type ustcTestQuotaRefresher struct{ used float64 }

func (f ustcTestQuotaRefresher) RefreshForScheduling(_ context.Context, account *Account) (*Account, error) {
	copy := copyAccountForUserInfoQuotaRefresh(account)
	copy.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixWindows)] = []UserInfoBudgetWindow{{Limit: 10, WindowSpend: f.used, UsedKnown: true}}
	return copy, nil
}

func TestUSTCThreeKeysDistributeConcurrentTrafficWithoutStickyOverflow(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			svc, cache := ustcSchedulerFixture(3, batch)
			var workers sync.WaitGroup
			errors := make(chan error, 60)
			for range 60 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "same-session", "deepseek-flash", nil)
					if err != nil {
						errors <- err
						return
					}
					if !selection.ustcAdmission.commit(context.Background()) {
						errors <- fmt.Errorf("commit rejected")
					}
					selection.ustcAdmission.release() // Simulate closing the upstream body.
					selection.ReleaseFunc()
				}()
			}
			workers.Wait()
			close(errors)
			for err := range errors {
				require.NoError(t, err)
			}
			cache.mu.Lock()
			for _, count := range cache.used {
				require.Equal(t, 20, count)
			}
			require.Len(t, cache.used, 3)
			require.Empty(t, cache.tickets)
			cache.mu.Unlock()
			_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "same-session", "deepseek-flash", nil)
			retry, capacity := USTCPoolRetryAfter(err)
			require.True(t, capacity)
			require.GreaterOrEqual(t, retry, 59)
		})
	}
}
func TestUSTCAbandonedSelectionRefundsAndSentFailuresKeepRPM(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	ctx := context.Background()
	selection, err := svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	selection.ReleaseFunc()
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	value, err := cache.USTCRead(ctx, USTCKeyScope(&account), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Zero(t, value.Used)
	require.Zero(t, value.Pending)
	require.Zero(t, value.InFlight)
	selection, err = svc.SelectAccountWithLoadAwareness(ctx, nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	require.True(t, selection.ustcAdmission.commit(ctx))
	selection.ustcAdmission.release()
	selection.ReleaseFunc()
	value, err = cache.USTCRead(ctx, USTCKeyScope(&account), USTCLimits{20, 20})
	require.NoError(t, err)
	require.Equal(t, 1, value.Used)
	require.Zero(t, value.InFlight)
}
func TestUSTCDuplicateAccountRowsSharePhysicalKeyCapacity(t *testing.T) {
	svc, cache := ustcSchedulerFixture(3, false)
	repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
	for i := range repo.accounts {
		repo.accounts[i].Credentials["api_key"] = "same-physical-key"
	}
	for range 20 {
		selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
		require.NoError(t, err)
		require.True(t, selection.ustcAdmission.commit(context.Background()))
		selection.ustcAdmission.release()
		selection.ReleaseFunc()
	}
	_, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	cache.mu.Lock()
	require.Len(t, cache.used, 1)
	cache.mu.Unlock()
	oldScope := USTCKeyScope(&repo.accounts[0])
	repo.accounts[0].Credentials["api_key"] = "rotated-key"
	require.NotEqual(t, oldScope, USTCKeyScope(&repo.accounts[0]))
}
func TestUSTCUnknownLimitsNeverUseManualRPMOrUnlimitedFallback(t *testing.T) {
	svc, _ := ustcSchedulerFixture(1, false)
	account := &svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	delete(account.Extra, "upstream_userinfo_limits_known")
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.Nil(t, selection)
	_, capacity := USTCPoolRetryAfter(err)
	require.True(t, capacity)
	account.Extra["upstream_userinfo_limits_known"] = true
	svc.rpmCache = nil
	selection, err = svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.Nil(t, selection)
	_, capacity = USTCPoolRetryAfter(err)
	require.True(t, capacity)
}

type ustcTransportStub struct {
	HTTPUpstream
	calls int
}

func (s *ustcTransportStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	s.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")), Request: req}, nil
}
func TestUSTCHTTPTransportCommitsAtSendAndReleasesBodyForEveryAttempt(t *testing.T) {
	svc, cache := ustcSchedulerFixture(1, false)
	upstream := &ustcTransportStub{}
	svc.httpUpstream = upstream
	account := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
	selection, err := svc.SelectAccountWithLoadAwareness(context.Background(), nil, "", "deepseek-flash", nil)
	require.NoError(t, err)
	value, _ := cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Zero(t, value.Used)
	require.Equal(t, 1, value.Pending)
	req, _ := http.NewRequestWithContext(ContextWithUSTCAdmission(context.Background(), selection), "POST", "https://api.llm.ustc.edu.cn/v1/chat/completions", nil)
	resp, err := svc.doOpenAIUpstream(req, "", &account)
	require.NoError(t, err)
	value, _ = cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Equal(t, 1, value.Used)
	require.Equal(t, 1, value.InFlight)
	require.NoError(t, resp.Body.Close())
	resp, err = svc.doOpenAIUpstream(req, "", &account)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	selection.ReleaseFunc()
	value, _ = cache.USTCRead(context.Background(), USTCKeyScope(&account), USTCLimits{20, 20})
	require.Equal(t, 2, value.Used)
	require.Zero(t, value.InFlight)
	require.Equal(t, 2, upstream.calls)
}
