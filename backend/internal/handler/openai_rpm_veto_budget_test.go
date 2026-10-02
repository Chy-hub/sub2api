//go:build unit

package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// Return valid quota during selection, then reject admission. Many unused
// accounts remain available, so only the veto budget can terminate each loop.
type admissionVetoQuotaRefresher struct {
	mu       sync.Mutex
	held     map[int64]bool
	vetoed   map[int64]struct{}
	released int
}

func (f *admissionVetoQuotaRefresher) RefreshForScheduling(_ context.Context, account *service.Account) (*service.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cloned := *account
	valid := !f.held[account.ID]
	cloned.Extra = map[string]any{"upstream_userinfo_limits_known": true, "upstream_userinfo_rpm_limit": 20, "upstream_userinfo_max_parallel_requests": 20, "upstream_userinfo_valid": valid}
	if !valid {
		f.vetoed[account.ID] = struct{}{}
	}
	return &cloned, nil
}

type admissionVetoConcurrencyCache struct {
	concurrencyCacheMock
	refresher *admissionVetoQuotaRefresher
}

type admissionCapacityCache struct {
	service.RPMCache
	service.USTCCapacityCache
	exhausted bool
}

func (c *admissionCapacityCache) USTCRead(context.Context, string, service.USTCLimits) (service.USTCCapacity, error) {
	limit := 20
	capacity := service.USTCCapacity{State: "ready", RPMLimit: &limit, ParallelLimit: &limit, Available: 20, ResetAt: time.Now().Add(time.Minute)}
	if c.exhausted {
		capacity.Used = 20
		capacity.Available = 0
	}
	return capacity, nil
}
func (c *admissionCapacityCache) USTCReserve(ctx context.Context, scope string, limits service.USTCLimits) (*service.USTCTicket, service.USTCCapacity, error) {
	value, err := c.USTCRead(ctx, scope, limits)
	if c.exhausted {
		return nil, value, err
	}
	return &service.USTCTicket{Scope: scope, ID: "test-ticket", Epoch: 1}, value, nil
}
func (*admissionCapacityCache) USTCRelease(context.Context, *service.USTCTicket) error { return nil }

func (c *admissionVetoConcurrencyCache) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	c.refresher.mu.Lock()
	defer c.refresher.mu.Unlock()
	c.refresher.held[id] = true
	return true, nil
}

func (c *admissionVetoConcurrencyCache) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.refresher.mu.Lock()
	defer c.refresher.mu.Unlock()
	delete(c.refresher.held, id)
	c.refresher.released++
	return nil
}

func TestOpenAIHTTPUSTCAdmissionVetoHandlesBudgetAndPoolExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		path   string
		body   string
		handle func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/openai/v1/responses", `{"model":"deepseek-flash","input":"hello"}`, (*OpenAIGatewayHandler).Responses},
		{"messages", "/openai/v1/messages", `{"model":"deepseek-flash","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"chat", "/openai/v1/chat/completions", `{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).ChatCompletions},
		{"embeddings", "/openai/v1/embeddings", `{"model":"deepseek-flash","input":"hello"}`, (*OpenAIGatewayHandler).Embeddings},
		{"images", "/openai/v1/images/generations", `{"model":"gpt-image-1","prompt":"hello"}`, (*OpenAIGatewayHandler).Images},
		{"alpha_search", "/openai/v1/alpha/search", `{"model":"deepseek-flash","query":"hello"}`, (*OpenAIGatewayHandler).AlphaSearch},
		{"seedance", "/v1/seedance/tasks", `{"model":"doubao-seedance-2-0","content":[{"type":"text","text":"hello"}]}`, (*OpenAIGatewayHandler).SeedanceTasks},
	} {
		for _, admission := range []string{"quota_after_acquire"} {
			for _, poolSize := range []int{1, 30} {
				t.Run(fmt.Sprintf("%s/%s/pool_%d", tc.name, admission, poolSize), func(t *testing.T) {
					accounts := make([]service.Account, poolSize)
					for i := range accounts {
						accounts[i] = service.Account{ID: int64(i + 1), Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
							Status: service.StatusActive, Schedulable: true, Concurrency: 1,
							Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "test"},
							Extra:       map[string]any{"upstream_userinfo_limits_known": true, "upstream_userinfo_rpm_limit": 20, "upstream_userinfo_max_parallel_requests": 20},
						}
						if tc.name == "seedance" {
							accounts[i].Credentials["openai_capabilities"] = []string{"seedance"}
						}
					}
					cfg := &config.Config{RunMode: config.RunModeSimple}
					cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
					billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
					t.Cleanup(billing.Stop)
					refresher := &admissionVetoQuotaRefresher{held: make(map[int64]bool), vetoed: make(map[int64]struct{})}
					concurrency := service.NewConcurrencyService(&admissionVetoConcurrencyCache{refresher: refresher})
					selectionConcurrency := concurrency
					gateway := service.NewOpenAIGatewayService(&openAIWSFailoverHandlerAccountRepoStub{accounts: accounts},
						nil, nil, nil, nil, nil, nil, cfg, nil, selectionConcurrency, service.NewBillingService(cfg, nil), nil, billing, nil,
						&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
					gateway.SetRPMCache(&admissionCapacityCache{})
					gateway.SetUSTCQuotaRefresher(refresher)
					h := NewOpenAIGatewayHandler(gateway, concurrency, billing,
						service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
					w := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(w)
					c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
					c.Request.Header.Set("Content-Type", "application/json")
					groupID := int64(7)
					c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 10, GroupID: &groupID,
						User:  &service.User{ID: 11, Status: service.StatusActive},
						Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowImageGeneration: true, AllowMessagesDispatch: true}})
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11, Concurrency: 0})
					tc.handle(h, c)
					require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
					require.Equal(t, "gateway_account_limit", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
					require.NotEmpty(t, w.Header().Get("Retry-After"), "all HTTP admission veto exits must provide a retry hint")
					wantVetoes := min(poolSize, maxUSTCAdmissionVetoAttempts)
					require.Len(t, refresher.vetoed, wantVetoes)
					require.Equal(t, wantVetoes, refresher.released, "every rejected slot must be released")
					require.Empty(t, refresher.held)
					if poolSize > maxUSTCAdmissionVetoAttempts {
						require.Less(t, len(refresher.vetoed), len(accounts))
					}
				})
			}
		}
	}
}

func TestOpenAIUSTCAdmissionVetoExhaustedTerminatesStartedStreams(t *testing.T) {
	for _, tc := range []struct{ path, event string }{
		{"/v1/responses", "event: response.failed"},
		{"/v1/messages", "event: error"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Writer.WriteHeaderNow()
			(&OpenAIGatewayHandler{}).handleUSTCAdmissionVetoExhausted(c, true, zap.NewNop(), maxUSTCAdmissionVetoAttempts)
			require.Contains(t, w.Body.String(), tc.event)
			require.Contains(t, w.Body.String(), "gateway_account_limit")
			require.Equal(t, http.StatusOK, w.Code)
		})
	}
}

func TestUSTCPoolCapacityTimeoutReturns429WithRetryAfterBeforeForwarding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		path   string
		body   string
		handle func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"messages", "/v1/messages", `{"model":"deepseek-flash","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"responses", "/v1/responses", `{"model":"deepseek-flash","input":"hello"}`, (*OpenAIGatewayHandler).Responses},
		{"chat", "/v1/chat/completions", `{"model":"deepseek-flash","messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).ChatCompletions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.Scheduling.FallbackMaxWaiting = 100
			cfg.Gateway.Scheduling.FallbackWaitTimeout = 20 * time.Millisecond
			cfg.Gateway.Scheduling.FallbackMaxWaiting = 1
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			accounts := []service.Account{{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				Status: service.StatusActive, Schedulable: true, Concurrency: 20,
				Credentials: map[string]any{"base_url": "https://api.llm.ustc.edu.cn", "api_key": "test"},
				Extra:       map[string]any{"upstream_userinfo_limits_known": true, "upstream_userinfo_rpm_limit": 20, "upstream_userinfo_max_parallel_requests": 20},
			}}
			concurrency := service.NewConcurrencyService(&concurrencyCacheMock{})
			gateway := service.NewOpenAIGatewayService(&openAIWSFailoverHandlerAccountRepoStub{accounts: accounts},
				nil, nil, nil, nil, nil, nil, cfg, nil, concurrency, service.NewBillingService(cfg, nil), nil, billing, nil,
				&service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
			gateway.SetRPMCache(&admissionCapacityCache{exhausted: true})
			h := NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := int64(9)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 10, GroupID: &groupID,
				User:  &service.User{ID: 11, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true}})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11, Concurrency: 0})
			tc.handle(h, c)
			require.Equal(t, http.StatusTooManyRequests, w.Code, w.Body.String())
			require.Equal(t, "gateway_account_limit", gjson.GetBytes(w.Body.Bytes(), "error.code").String())
			require.NotEmpty(t, w.Header().Get("Retry-After"))
			require.NotContains(t, w.Body.String(), "Service temporarily unavailable")
		})
	}
}
