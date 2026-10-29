package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type reverseFixtureProvider struct {
	forward      func(context.Context, *reverse.Account, []byte, bool) (*http.Response, error)
	checkin      *reverse.CheckinResult
	models       []reverse.Model
	testModel    string
	run          func(context.Context, *reverse.Account, string) (reverse.TaskRunResult, error)
	scheduled    func(context.Context, *reverse.Account, string) (string, error)
	refreshCalls int
	allCalls     int
}

func (p *reverseFixtureProvider) Forward(ctx context.Context, acc *reverse.Account, body []byte, stream bool) (*http.Response, error) {
	return p.forward(ctx, acc, body, stream)
}
func (p *reverseFixtureProvider) ListModels(context.Context, *reverse.Account) ([]reverse.Model, error) {
	if p.models != nil {
		return p.models, nil
	}
	return []reverse.Model{{ID: "test-model"}}, nil
}
func (p *reverseFixtureProvider) Checkin(context.Context, *reverse.Account) (*reverse.CheckinResult, error) {
	return p.checkin, nil
}
func (p *reverseFixtureProvider) Refresh(context.Context, *reverse.Account) (map[string]any, error) {
	p.refreshCalls++
	return map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"}, nil
}
func (p *reverseFixtureProvider) Test(_ context.Context, _ *reverse.Account, model, _ string) (*reverse.TestResult, error) {
	p.testModel = model
	return &reverse.TestResult{Content: "OK"}, nil
}
func (*reverseFixtureProvider) Quota(context.Context, *reverse.Account) (*reverse.QuotaFetchResponse, error) {
	return &reverse.QuotaFetchResponse{Summary: []reverse.QuotaMetric{{Key: "remaining", Value: 42}}}, nil
}
func (*reverseFixtureProvider) TaskSpecs(*reverse.Account) []reverse.TaskSpec {
	return []reverse.TaskSpec{{Kind: "travel", DefaultHours: []int{9, 21}}}
}
func (*reverseFixtureProvider) InspectTasks(context.Context, *reverse.Account) (reverse.TaskInspection, error) {
	return reverse.TaskInspection{}, nil
}
func (p *reverseFixtureProvider) RunTask(ctx context.Context, acc *reverse.Account, code string) (reverse.TaskRunResult, error) {
	if p.run != nil {
		return p.run(ctx, acc, code)
	}
	return reverse.TaskRunResult{Status: "done"}, nil
}
func (p *reverseFixtureProvider) RunAllTasks(context.Context, *reverse.Account) []reverse.AutoTaskResult {
	p.allCalls++
	return []reverse.AutoTaskResult{{Code: "one", Status: "done"}, {Code: "two", Status: "error", Message: "upstream rejected claim"}}
}
func (p *reverseFixtureProvider) RunScheduled(ctx context.Context, acc *reverse.Account, kind string) (string, error) {
	if p.scheduled != nil {
		return p.scheduled(ctx, acc, kind)
	}
	return "done", nil
}
func (*reverseFixtureProvider) TaskStatus(context.Context, *reverse.Account) reverse.TaskStatusResult {
	return reverse.TaskStatusResult{}
}

type reverseFixtureRepo struct {
	AccountRepository
	account  *Account
	model    string
	extraErr error
}

func (r *reverseFixtureRepo) GetByID(context.Context, int64) (*Account, error) {
	copy := *r.account
	return &copy, nil
}
func (r *reverseFixtureRepo) UpdateCredentials(_ context.Context, _ int64, creds map[string]any) error {
	r.account.Credentials = creds
	return nil
}
func (r *reverseFixtureRepo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	if r.extraErr != nil {
		return r.extraErr
	}
	if r.account.Extra == nil {
		r.account.Extra = map[string]any{}
	}
	for key, value := range extra {
		r.account.Extra[key] = value
	}
	return nil
}
func (r *reverseFixtureRepo) SetModelRateLimit(_ context.Context, _ int64, model string, _ time.Time, _ ...string) error {
	r.model = model
	return nil
}
func (r *reverseFixtureRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	if r.account.Platform == platform {
		return []Account{*r.account}, nil
	}
	return nil, nil
}

func newReverseFixture() (*ReverseGateway, *reverseFixtureProvider, *Account, *reverseFixtureRepo) {
	account := &Account{ID: 1, Platform: PlatformWorkbuddyCN, Type: AccountTypeReverse, Status: StatusActive, Credentials: map[string]any{"access_token": "old-access", "refresh_token": "old-refresh", "machine_token": "machine-token", "machine_type": "machine-type"}, Extra: map[string]any{}}
	repo := &reverseFixtureRepo{account: account}
	gateway := NewReverseGateway(repo)
	provider := &reverseFixtureProvider{}
	gateway.registry.Register("workbuddy", provider)
	gateway.registry.Register("qoder", provider)
	return gateway, provider, account, repo
}

func TestReverseGatewayAllIngressProtocolsPreserveUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{PlatformQoderCN, PlatformQoderGlobal, PlatformWorkbuddyCN, PlatformWorkbuddyGlobal} {
		for _, protocol := range []string{"chat/completions", "responses", "messages"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", platform, protocol, stream), func(t *testing.T) {
					gateway, provider, account, _ := newReverseFixture()
					account.Platform = platform
					account.Credentials["model_mapping"] = map[string]any{"client-alias": "test-model"}
					require.True(t, account.IsModelSupported("client-alias"))
					require.False(t, account.IsModelSupported("not-allowed"))
					called := 0
					provider.forward = func(ctx context.Context, acc *reverse.Account, body []byte, wantsStream bool) (*http.Response, error) {
						called++
						require.Equal(t, platform, acc.Platform)
						require.Equal(t, "test-model", gjson.GetBytes(body, "model").String())
						require.Contains(t, string(body), "hello")
						messages := gjson.GetBytes(body, "messages").Array()
						require.Len(t, messages, 1, "the gateway must not add default instructions")
						require.Equal(t, "user", messages[0].Get("role").String())
						payload := `{"id":"chatcmpl-test","object":"chat.completion","model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}`
						contentType := "application/json"
						if wantsStream {
							payload = "data: " + `{"id":"chatcmpl-test","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}` + "\n\ndata: " + `{"id":"chatcmpl-test","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}` + "\n\ndata: [DONE]\n\n"
							contentType = "text/event-stream"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
					}
					body := fmt.Sprintf(`{"model":"client-alias","stream":%v,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`, stream)
					if protocol == "responses" {
						body = fmt.Sprintf(`{"model":"client-alias","stream":%v,"input":"hello"}`, stream)
					}
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, strings.NewReader(body))
					svc := &OpenAIGatewayService{cfg: structuredInputRetryTestConfig(), reverseGateway: gateway}
					var result *OpenAIForwardResult
					var err error
					switch protocol {
					case "chat/completions":
						result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), "", "")
					case "messages":
						result, err = svc.ForwardAsAnthropic(context.Background(), c, account, []byte(body), "", "")
					case "responses":
						result, err = svc.Forward(context.Background(), c, account, []byte(body))
					}
					require.NoError(t, err, recorder.Body.String())
					require.Equal(t, 1, called)
					require.Contains(t, recorder.Body.String(), "OK")
					require.Equal(t, 9, result.Usage.InputTokens)
					require.Equal(t, 3, result.Usage.OutputTokens)
					require.Equal(t, 4, result.Usage.CacheReadInputTokens)
				})
			}
		}
	}
}

func TestReverseFailedCheckinNeverRecordsSuccess(t *testing.T) {
	g, p, a, _ := newReverseFixture()
	p.checkin = &reverse.CheckinResult{Success: false, Message: "no campaign", Details: map[string]any{"not_recorded": true}}
	_, state, err := g.Checkin(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "failed", state["status"])
	require.NotContains(t, state, "last_success_at")
	last := reverseCheckinLastAttempt(a.Extra)
	require.False(t, reverseCheckinDue(a.Extra, a.Platform, last.Add(time.Minute)))
	require.True(t, reverseCheckinDue(a.Extra, a.Platform, last.Add(11*time.Minute)))
	p.checkin.Success = true
	_, state, err = g.Checkin(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "success", state["status"])
	require.Contains(t, state, "last_success_at")
	encoded, err := json.Marshal(p.checkin)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(encoded, "success").Bool())
}

func TestReverseAccountTestAppliesConfiguredModelMapping(t *testing.T) {
	g, p, account, _ := newReverseFixture()
	account.Credentials["model_mapping"] = map[string]any{"client-alias": "upstream-model"}
	_, err := g.Test(context.Background(), account, "client-alias", "hello")
	require.NoError(t, err)
	require.Equal(t, "upstream-model", p.testModel)
}

func TestReverseRefreshPreservesMachineParametersAndAvoidsStaleRotation(t *testing.T) {
	g, p, a, repo := newReverseFixture()
	snapshot := *a
	_, err := g.Refresh(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "new-access", a.GetCredential("access_token"))
	require.Equal(t, "machine-token", a.GetCredential("machine_token"))
	require.NoError(t, g.refreshAccount(context.Background(), &snapshot))
	require.Equal(t, 1, p.refreshCalls)
	require.Equal(t, "new-refresh", repo.account.GetCredential("refresh_token"))
}

func TestReverseTaskPartialFailurePersistsAndSerializesOperations(t *testing.T) {
	g, p, a, _ := newReverseFixture()
	result, err := g.RunTask(context.Background(), a, ReverseTaskRequest{Action: "all"})
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Len(t, result.Results, 2)
	require.Equal(t, result, a.Extra[ReverseTasksExtraKey])
	unlocked, err := g.lockOperation(context.Background(), a.ID)
	require.NoError(t, err)
	_, err = g.RunTask(context.Background(), a, ReverseTaskRequest{Action: "all"})
	require.ErrorIs(t, err, ErrReverseOperationBusy)
	unlocked()
	require.Equal(t, 1, p.allCalls)
	p.run = func(context.Context, *reverse.Account, string) (reverse.TaskRunResult, error) {
		return reverse.TaskRunResult{}, errors.New("claim failed")
	}
	result, err = g.RunTask(context.Background(), a, ReverseTaskRequest{Action: "run", Code: "task"})
	require.ErrorContains(t, err, "claim failed")
	require.False(t, result.Success)
}

func TestReverseTasksAreDisabledByDefault(t *testing.T) {
	g, p, a, repo := newReverseFixture()
	s := NewReverseCheckinScheduler(repo, g, time.Minute)
	defer s.Stop()
	s.runOnce()
	require.Equal(t, 0, p.allCalls)
	require.Equal(t, 0, p.refreshCalls)
	input := &CreateAccountInput{Platform: PlatformWorkbuddyCN, Type: AccountTypeReverse, Credentials: map[string]any{"accessToken": "access"}}
	created, err := buildAccountForCreate(input, map[string]any{})
	require.NoError(t, err)
	require.NotEmpty(t, created.GetCredential("machine_salt"))
	require.NotEqual(t, true, a.Extra[ReverseAutoTasksExtraKey])
}

func TestReverseTaskScheduleUsesSlotsAndSuccessfulCheckin(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 10, 29, 21, 5, 0, 0, loc)
	spec := reverse.TaskSpec{Kind: "travel", DefaultHours: []int{9, 21}}
	extra := map[string]any{ReverseTaskScheduleExtraKey: map[string]any{"travel": now.Add(-12 * time.Hour).Format(time.RFC3339)}}
	require.True(t, reverseTaskDue(extra, spec, now))
	extra[ReverseTaskScheduleExtraKey].(map[string]any)["travel"] = now.Format(time.RFC3339)
	require.False(t, reverseTaskDue(extra, spec, now.Add(time.Minute)))
	spec = reverse.TaskSpec{Kind: "streak", AfterCheckin: true}
	require.False(t, reverseTaskDue(extra, spec, now))
	extra[ReverseCheckinExtraKey] = map[string]any{"status": "success", "last_success_at": now.Format(time.RFC3339)}
	require.True(t, reverseTaskDue(extra, spec, now))
}

func TestReverseScheduledTaskReservesSlotBeforeSideEffectsAndRechecksLatestState(t *testing.T) {
	g, p, a, repo := newReverseFixture()
	a.Extra[ReverseAutoTasksExtraKey] = true
	stale := *a
	stale.Extra = map[string]any{ReverseAutoTasksExtraKey: true}
	loc := time.FixedZone("UTC+8", 8*3600)
	now := time.Date(2026, 10, 29, 20, 59, 0, 0, loc)
	spec := reverse.TaskSpec{Kind: "travel", DefaultHours: []int{20, 21}}
	calls := 0
	p.scheduled = func(_ context.Context, acc *reverse.Account, kind string) (string, error) {
		calls++
		schedule := repo.account.Extra[ReverseTaskScheduleExtraKey].(map[string]any)
		require.Equal(t, now.Truncate(time.Hour).UTC().Format(time.RFC3339), schedule[kind])
		return "one step completed", errors.New("upstream unavailable")
	}
	require.ErrorContains(t, g.runScheduledTask(context.Background(), a, spec, now), "upstream unavailable")
	require.NoError(t, g.runScheduledTask(context.Background(), &stale, spec, now))
	require.Equal(t, 1, calls)
	// Finishing after 21:00 cannot consume the next slot. Only 20:00 was reserved.
	require.True(t, reverseTaskDue(repo.account.Extra, spec, now.Add(2*time.Minute)))
	repo.account.Extra[ReverseAutoTasksExtraKey] = false
	require.NoError(t, g.runScheduledTask(context.Background(), &stale, spec, now.Add(2*time.Minute)))
	require.Equal(t, 1, calls)
	last := repo.account.Extra[ReverseTasksExtraKey].(*ReverseTaskOutcome)
	require.False(t, last.Success)
	require.Contains(t, last.Message, "one step completed")
	require.Contains(t, last.Message, "upstream unavailable")
}

func TestReverseScheduledTaskDoesNotRunWhenSlotCannotBeSaved(t *testing.T) {
	g, p, a, repo := newReverseFixture()
	a.Extra[ReverseAutoTasksExtraKey] = true
	repo.extraErr = errors.New("database unavailable")
	p.scheduled = func(context.Context, *reverse.Account, string) (string, error) {
		t.Fatal("upstream action must follow a persisted reservation")
		return "", nil
	}
	err := g.runScheduledTask(context.Background(), a, reverse.TaskSpec{Kind: "travel", DefaultHours: []int{0}}, time.Now())
	require.ErrorContains(t, err, "database unavailable")
	require.NotContains(t, a.Extra, ReverseTaskScheduleExtraKey)
}

func TestReverseTaskPersistenceFailureCannotReportSuccess(t *testing.T) {
	g, _, a, repo := newReverseFixture()
	repo.extraErr = errors.New("database unavailable")
	result, err := g.RunTask(context.Background(), a, ReverseTaskRequest{Action: "run", Code: "one"})
	require.ErrorContains(t, err, "database unavailable")
	require.False(t, result.Success)
	require.Contains(t, result.Message, "failed to save")
}

func TestReverseAccountOperationsSerializeAcrossGatewayInstances(t *testing.T) {
	g, _, a, repo := newReverseFixture()
	peer := NewReverseGateway(repo)
	cache := &fakeLeaderLockCache{}
	g.lockCache, peer.lockCache = cache, cache
	unlock, err := g.lockOperation(context.Background(), a.ID)
	require.NoError(t, err)
	_, err = peer.lockOperation(context.Background(), a.ID)
	require.ErrorIs(t, err, ErrReverseOperationBusy)
	unlock()
	unlock, err = peer.lockOperation(context.Background(), a.ID)
	require.NoError(t, err)
	unlock()
}

func TestReverseLoginTicketCanRetryFailedCreationAndRemainsSingleUse(t *testing.T) {
	g, _, _, _ := newReverseFixture()
	g.tickets["ticket"] = &reverseLoginTicket{Platform: PlatformQoderGlobal, Credentials: map[string]any{"access_token": "access"}, ExpiresAt: time.Now().Add(time.Minute)}
	platform, creds, finish, ok := g.AcquireLoginTicket(" ticket ")
	require.True(t, ok)
	require.Equal(t, PlatformQoderGlobal, platform)
	require.Equal(t, "access", creds["access_token"])
	creds["access_token"] = "local edit"
	_, _, _, ok = g.AcquireLoginTicket("ticket")
	require.False(t, ok, "a reserved ticket cannot create two accounts concurrently")
	finish(false)
	_, creds, commit, ok := g.AcquireLoginTicket("ticket")
	require.True(t, ok)
	require.Equal(t, "access", creds["access_token"])
	commit(true)
	finish(false)
	_, _, _, ok = g.AcquireLoginTicket("ticket")
	require.False(t, ok, "successful creation must consume the ticket permanently")
}

func TestReverseModelPreviewDoesNotConsumeTicketOrPersistTemporaryCatalog(t *testing.T) {
	g, _, saved, _ := newReverseFixture()
	g.tickets["ticket"] = &reverseLoginTicket{Platform: PlatformQoderGlobal, Credentials: map[string]any{"access_token": "dt-browser-access", "uid": "browser-user"}, ExpiresAt: time.Now().Add(time.Minute)}
	credentials := map[string]any{"machine_token": "account-machine", "machine_type": "account-type", "model_mapping": map[string]any{"client-alias": "test-model"}}
	preview, err := g.PreviewAccount(PlatformQoderGlobal, credentials, "ticket")
	require.NoError(t, err)
	require.Zero(t, preview.ID)
	require.Equal(t, "dt-browser-access", preview.GetCredential("access_token"))
	require.Equal(t, "account-machine", preview.GetCredential("machine_token"))
	require.Equal(t, "test-model", preview.GetModelMapping()["client-alias"])
	_, err = g.ListModels(context.Background(), preview)
	require.NoError(t, err)
	require.Empty(t, saved.Extra, "a temporary preview must not write a catalog to a stored account")
	require.NotContains(t, credentials, "access_token", "preview must not mutate the form credentials")
	_, _, finish, ok := g.AcquireLoginTicket("ticket")
	require.True(t, ok, "the same ticket must still be usable to create an account")
	finish(true)
}

func TestReverseModelPreviewRejectsMissingExpiredOrMismatchedCredentials(t *testing.T) {
	g, _, _, _ := newReverseFixture()
	g.tickets["expired"] = &reverseLoginTicket{Platform: PlatformQoderCN, ExpiresAt: time.Now().Add(-time.Minute)}
	g.tickets["other-region"] = &reverseLoginTicket{Platform: PlatformQoderGlobal, ExpiresAt: time.Now().Add(time.Minute)}
	_, err := g.PreviewAccount(PlatformQoderCN, nil, "expired")
	require.ErrorIs(t, err, ErrReverseLoginTicketInvalid)
	_, err = g.PreviewAccount(PlatformQoderCN, nil, "other-region")
	require.ErrorIs(t, err, ErrReverseLoginPlatformMismatch)
	_, err = g.PreviewAccount(PlatformQoderCN, nil, "")
	require.ErrorContains(t, err, "access_token")
	preview, err := g.PreviewAccount(PlatformWorkbuddyCN, map[string]any{"auth": map[string]any{"accessToken": "manual-access"}}, "")
	require.NoError(t, err)
	require.Equal(t, "manual-access", preview.GetCredential("access_token"))
}

func TestReverseModelFailuresOnlyCoolDownOneModel(t *testing.T) {
	for _, failure := range []*reverse.UpstreamError{{StatusCode: 400, Code: "11102", Kind: string(reverse.FailureKindModelBlocked), Message: "no such model"}, {StatusCode: 429, Code: "6004", Message: "model rate limited"}} {
		g, p, a, repo := newReverseFixture()
		p.forward = func(context.Context, *reverse.Account, []byte, bool) (*http.Response, error) {
			copy := *failure
			return nil, &copy
		}
		resp, err := g.Forward(context.Background(), a, []byte(`{"model":"model-one"}`), false)
		require.NoError(t, err)
		require.Equal(t, 400, resp.StatusCode)
		require.Equal(t, "model-one", repo.model)
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.True(t, isOpenAICompatibleModelNotFoundBody(raw))
	}
}

func TestReverseModelSyncPreservesNativeCapabilitiesAndCreditMetadata(t *testing.T) {
	g, p, a, repo := newReverseFixture()
	p.models = []reverse.Model{
		{ID: "reasoning-model", DisplayName: "Reasoning Model", ContextLength: 200000, MaxOutputTokens: 16000, Efforts: []string{"low", "high"}, DefaultEffort: "high", SupportsImages: true, Credits: "0"},
		{ID: "text-model", ContextLength: 64000, MaxOutputTokens: 8000, Efforts: []string{"none"}},
	}
	svc := &AccountTestService{reverseGateway: g, accountRepo: repo}
	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), a)
	require.NoError(t, err)
	require.Empty(t, catalog.Warnings)
	metadata := catalog.Metadata["reasoning-model"]
	require.EqualValues(t, 200000, metadata.ContextWindow)
	require.EqualValues(t, 16000, metadata.MaxOutputTokens)
	require.Equal(t, []string{"low", "high"}, metadata.SupportedReasoningLevels)
	require.Equal(t, "high", metadata.DefaultReasoningLevel)
	require.Equal(t, []string{"text", "image"}, metadata.InputModalities)
	require.True(t, *metadata.Reasoning)
	require.False(t, *catalog.Metadata["text-model"].Reasoning)
	snapshot := a.GetUpstreamModelMetadataSnapshot()
	require.NotNil(t, snapshot)
	require.Len(t, snapshot.Models, 2)
	stored := repo.account.Extra[ReverseModelCatalogExtraKey].(map[string]any)["catalog"].([]reverse.Model)
	require.Equal(t, "0", stored[0].Credits)
}

func TestReverseCredentialAliasesAndValidation(t *testing.T) {
	input := map[string]any{"auth": map[string]any{"accessToken": "access", "refreshToken": "refresh"}, "qoder_machine_token": "machine", "qoder_machine_type": "type", "qoder_desktop_version": "0.5.0"}
	creds, err := normalizeReverseCredentials(PlatformQoderGlobal, input)
	require.NoError(t, err)
	require.Equal(t, "access", creds["access_token"])
	require.Equal(t, "machine", creds["machine_token"])
	require.Equal(t, "0.5.0", creds["desktop_version"])
	require.NotContains(t, creds, "auth")
	require.NotContains(t, creds, "qoder_machine_token")
	_, err = normalizeReverseCredentials(PlatformQoderCN, map[string]any{"machine_token": "half"})
	require.Error(t, err)
	require.Error(t, validateReverseExtra(PlatformWorkbuddyCN, map[string]any{"reverse_task_hours": map[string]any{"growth": []any{24.0}}}))
	require.NoError(t, validateReverseExtra(PlatformWorkbuddyCN, map[string]any{"reverse_task_hours": map[string]any{"growth": []any{9.0, 21.0}}}))
	_, err = normalizeReverseCredentials(PlatformWorkbuddyCN, map[string]any{"user_agent": "value\r\nInjected: bad"})
	require.Error(t, err)
}

func TestReverseQoderExportSecretNormalizesAndMachineIdentityCanBeCleared(t *testing.T) {
	credentials, err := normalizeReverseCredentials(PlatformQoderGlobal, map[string]any{"secret": `{"device_token":"dt-access","refresh_token":"drt-refresh"}`, "machine_token": "device-identity", "machine_type": "device-type"})
	require.NoError(t, err)
	require.Equal(t, "dt-access", credentials["access_token"])
	require.Equal(t, "drt-refresh", credentials["refresh_token"])
	require.NotContains(t, credentials, "secret")
	updated := MergePreservingSensitiveCreds(credentials, map[string]any{"access_token": "dt-access", "machine_token": "", "machine_type": ""})
	credentials, err = normalizeReverseCredentials(PlatformQoderGlobal, updated)
	require.NoError(t, err)
	require.Empty(t, credentials["machine_token"])
	require.Empty(t, credentials["machine_type"])
	_, err = normalizeReverseCredentials(PlatformQoderCN, map[string]any{"vendor": "qoderglobal", "token": "dt-access"})
	require.Error(t, err)
}

func TestReverseLegacyIncompleteMachinePairCanBeRepaired(t *testing.T) {
	old, err := normalizeReverseCredentialFields(PlatformQoderGlobal, map[string]any{"access_token": "dt-old", "qoder_machine_token": "saved-machine"})
	require.NoError(t, err)
	merged := MergePreservingSensitiveCreds(old, map[string]any{"access_token": "dt-old", "machine_type": "new-type"})
	updated, err := normalizeReverseCredentials(PlatformQoderGlobal, merged)
	require.NoError(t, err)
	require.Equal(t, "saved-machine", updated["machine_token"])
	require.Equal(t, "new-type", updated["machine_type"])
}
