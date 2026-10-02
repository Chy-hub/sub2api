package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_Hit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{
		ID:          2,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_1", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_1", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	require.True(t, selection.Acquired)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_QuotaAutoPausedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{
		ID:          77,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 2,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
			"codex_5h_used_percent":                         96.0,
			"auto_pause_5h_threshold":                       0.95,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_quota", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_quota", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "超过 5h 配额阈值的账号不应继续命中 previous_response_id 粘连")

	// Auto-pause is transient, so the binding is preserved: the chain can resume on the
	// same account once the quota window resets.
	boundAccountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_prev_quota")
	require.NoError(t, getErr)
	require.Equal(t, account.ID, boundAccountID)
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_RateLimitedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	account := Account{
		ID:               12,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		RateLimitResetAt: &rateLimitedUntil,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_rl", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_rl", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "限额中的账号不应继续命中 previous_response_id 粘连")
	boundAccountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_prev_rl")
	require.NoError(t, getErr)
	require.Zero(t, boundAccountID)
}

func TestOpenAIGatewayService_ResolveAccountIDByPreviousResponseIDForScheduler_USTCCooldownsKeepBinding(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2301)
	future := time.Now().Add(30 * time.Minute)
	cases := []struct {
		name   string
		mutate func(*Account)
	}{
		{
			name: "rate_limit",
			mutate: func(account *Account) {
				account.RateLimitResetAt = &future
			},
		},
		{
			name: "overload",
			mutate: func(account *Account) {
				account.OverloadUntil = &future
			},
		},
		{
			name: "temporary_unschedulable",
			mutate: func(account *Account) {
				account.TempUnschedulableUntil = &future
				account.TempUnschedulableReason = "upstream_transport"
			},
		},
		{
			name: "model_cooldown",
			mutate: func(account *Account) {
				account.Extra[modelRateLimitsKey] = map[string]any{
					"gpt-5.1": map[string]any{
						"rate_limit_reset_at": future.UTC().Format(time.RFC3339),
					},
				}
			},
		},
		{
			name: "quota_exhausted",
			mutate: func(account *Account) {
				account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixBudget)] = 10.0
				account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixSpend)] = 10.0
				account.Extra[UserInfoQuotaExtraKey(UserInfoExtraSuffixResetAt)] = future.UTC().Format(time.RFC3339)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := Account{
				ID:          9301,
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				GroupIDs:    []int64{groupID},
				Credentials: map[string]any{
					"api_key":  "sk-ustc-sticky-test",
					"base_url": "https://api.llm.ustc.edu.cn/v1",
				},
				Extra: map[string]any{
					"openai_apikey_responses_websockets_v2_enabled": true,
				},
			}
			tc.mutate(&account)

			cache := &stubGatewayCache{}
			store := NewOpenAIWSStateStore(cache)
			svc := &OpenAIGatewayService{
				accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
				cache:              cache,
				openaiWSStateStore: store,
			}
			responseID := "resp_ustc_cooldown_" + tc.name
			require.NoError(t, store.BindResponseAccount(ctx, groupID, responseID, account.ID, time.Hour))

			resolvedID := svc.ResolveAccountIDByPreviousResponseIDForScheduler(
				ctx, &groupID, responseID, "gpt-5.1", nil, "", false,
			)
			require.Equal(t, account.ID, resolvedID, "temporary cooldown or quota must not erase the physical-Key sticky ID")
			boundID, err := store.GetResponseAccount(ctx, groupID, responseID)
			require.NoError(t, err)
			require.Equal(t, account.ID, boundID, "temporary unavailability must preserve the binding")
		})
	}
}

func TestOpenAIGatewayService_SelectUSTCPreviousResponseWithImageCapabilityStaysBound(t *testing.T) {
	ctx := context.Background()
	svc, capacityCache := ustcSchedulerFixture(2, false)
	store := NewOpenAIWSStateStore(svc.cache)
	svc.openaiWSStateStore = store
	responseID := "resp_ustc_image_capability"
	require.NoError(t, store.BindResponseAccount(ctx, 0, responseID, 1, time.Hour))

	selection, decision, err := svc.selectAccountWithScheduler(
		ctx,
		nil,
		responseID,
		"",
		"gpt-5.1",
		nil,
		OpenAIUpstreamTransportHTTPSSE,
		"",
		OpenAIImagesCapabilityAPIKey,
		false,
		PlatformOpenAI,
		false,
		false,
	)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(1), selection.Account.ID, "an image-capability requirement must still use the response's physical USTC Key")
	require.Equal(t, openAIAccountScheduleLayerPreviousResponse, decision.Layer)
	require.NotNil(t, selection.ustcAdmission)
	require.Equal(t, USTCKeyScope(selection.Account), selection.ustcAdmission.ticket.Scope)
	require.Len(t, capacityCache.tickets, 1, "the other USTC Key must not receive an admission ticket")
	selection.ReleaseFunc()
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_DBRuntimeRecheckRateLimitedMiss(t *testing.T) {
	ctx := context.Background()
	groupID := int64(24)
	rateLimitedUntil := time.Now().Add(30 * time.Minute)
	staleAccount := &Account{
		ID:          13,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	dbAccount := Account{
		ID:               13,
		Platform:         PlatformOpenAI,
		Type:             AccountTypeAPIKey,
		Status:           StatusActive,
		Schedulable:      true,
		Concurrency:      1,
		RateLimitResetAt: &rateLimitedUntil,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	snapshotCache := &openAISnapshotCacheStub{
		accountsByID: map[int64]*Account{dbAccount.ID: staleAccount},
	}
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{dbAccount}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
		schedulerSnapshot:  &SchedulerSnapshotService{cache: snapshotCache},
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_db_rl", dbAccount.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_db_rl", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "DB 中已限流的账号不应继续命中 previous_response_id 粘连")
	boundAccountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_prev_db_rl")
	require.NoError(t, getErr)
	require.Zero(t, boundAccountID)
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_Excluded(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{
		ID:          8,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_2", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_2", "gpt-5.1", map[int64]struct{}{account.ID: {}}, false)
	require.NoError(t, err)
	require.Nil(t, selection)
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_APIKeyForceHTTPHit(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{
		ID:          11,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_ws_force_http":            true,
			"responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_force_http", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_force_http", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection, "API-key HTTP continuation must retain the key/project that created the response")
	require.NotNil(t, selection.Account)
	require.Equal(t, account.ID, selection.Account.ID)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_OAuthForceHTTPIgnored(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{
		ID:          12,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Extra: map[string]any{
			"openai_ws_force_http":            true,
			"responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                newOpenAIWSV2TestConfig(),
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_oauth_force_http", account.ID, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_oauth_force_http", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection, "OAuth HTTP fallback cannot preserve WSv2 continuation state")
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_BusyKeepsSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	accounts := []Account{
		{
			ID:          21,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    0,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          22,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Priority:    9,
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
	}

	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 30 * time.Second

	concurrencyCache := stubConcurrencyCache{
		acquireResults: map[int64]bool{
			21: false, // previous_response 命中的账号繁忙
			22: true,  // 次优账号可用（若回退会命中）
		},
		waitCounts: map[int64]int{
			21: 999,
		},
	}

	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: accounts},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrencyCache),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_busy", 21, time.Hour))

	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_prev_busy", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, int64(21), selection.Account.ID, "busy previous_response sticky account should remain selected")
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, int64(21), selection.WaitPlan.AccountID)
}

func TestOpenAIGatewayService_SelectAccountByPreviousResponseID_CapabilityMismatchKeepsSticky(t *testing.T) {
	ctx := context.Background()
	groupID := int64(25)
	account := Account{
		ID:          31,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"openai_capabilities": []any{"chat_completions"},
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_enabled": true,
		},
	}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	cfg := newOpenAIWSV2TestConfig()
	svc := &OpenAIGatewayService{
		accountRepo:        stubOpenAIAccountRepo{accounts: []Account{account}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(stubConcurrencyCache{}),
		openaiWSStateStore: store,
	}

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_prev_capability", account.ID, time.Hour))

	selection, err := svc.selectAccountByPreviousResponseIDForCapability(
		ctx,
		&groupID,
		"resp_prev_capability",
		"text-embedding-3-small",
		nil,
		OpenAIEndpointCapabilityEmbeddings,
		false,
	)
	require.NoError(t, err)
	require.Nil(t, selection)
	boundAccountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_prev_capability")
	require.NoError(t, getErr)
	require.Equal(t, account.ID, boundAccountID)
}

func newOpenAIWSV2TestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
	return cfg
}
