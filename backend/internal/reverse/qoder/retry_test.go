package qoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
	"github.com/stretchr/testify/require"
)

func TestQueueErrorKeepsNestedWaitAndDoesNotBecomeCredentialFailure(t *testing.T) {
	inner, _ := json.Marshal(map[string]any{"isQueued": true, "modelKey": "qfmodel", "retryAfterSeconds": 30, "serviceAvailable": false})
	middle, _ := json.Marshal(map[string]any{"code": "10605", "message": string(inner)})
	body, _ := json.Marshal(map[string]any{"code": "403", "message": string(middle)})
	err := upstreamError(403, nil, body)
	require.Equal(t, 503, err.StatusCode)
	require.Equal(t, "upstream_model_busy", err.Code)
	require.Equal(t, 30*time.Second, err.RetryAfter)
	require.False(t, retryable(&reverse.UpstreamError{StatusCode: 401, Message: "invalid token"}))
	require.False(t, retryable(&reverse.UpstreamError{StatusCode: 500, Message: "DataInspectionFailed"}))
	require.False(t, retryable(&reverse.UpstreamError{StatusCode: 502, Code: "empty_upstream_stream"}))
	require.False(t, retryable(nil))
	for _, status := range []int{401, 403, 429} {
		require.False(t, retryable(&reverse.UpstreamError{StatusCode: status, Message: "provider_error"}), "status %d", status)
	}
}

func TestRetryDistinguishesTransportFailuresFromStreamBusinessErrors(t *testing.T) {
	require.True(t, retryable(&url.Error{Op: "Post", URL: "https://example.test", Err: io.EOF}))
	require.True(t, retryable(&url.Error{Op: "Post", URL: "https://example.test", Err: errors.New("connection reset by peer")}))
	require.False(t, retryable(&url.Error{Op: "Post", URL: "https://example.test", Err: context.Canceled}))
	for _, body := range []string{`{"code":"115","message":"business rejected"}`, `{"error":{"code":"invalid_request_error","message":"invalid input"}}`} {
		delta := extractDelta(wrapEnvelope(t, body))
		require.NotNil(t, delta.Err)
		require.False(t, retryable(delta.Err))
	}
	delta := extractDelta(wrapEnvelope(t, `{"error":null,"choices":[{"delta":{"content":"OK"}}]}`))
	require.Nil(t, delta.Err)
	require.Equal(t, "OK", delta.Content)
}

func TestContentAndParameterFailuresDoNotPenalizeAccountsAsServerErrors(t *testing.T) {
	for _, body := range []string{`{"message":"DataInspectionFailed"}`, `{"code":"invalid_parameter_error","message":"max_tokens out of range"}`} {
		err := upstreamError(http.StatusInternalServerError, nil, []byte(body))
		require.Equal(t, http.StatusBadRequest, err.StatusCode)
		require.False(t, retryable(err))
	}
}

func TestInspectStreamPreservesReadAheadAndSurfacesEarlyEnvelopeFailure(t *testing.T) {
	first := wrapEnvelope(t, `{"choices":[{"delta":{"content":"hello"}}]}`)
	source := "data: " + first + "\r\n\r\ndata: [DONE]\r\n\r\n"
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(source))}
	require.NoError(t, inspectStream(resp))
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, source, string(raw))
	failure := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + `{"statusCodeValue":418,"body":"provider_error"}` + "\n\n"))}
	var upstream *reverse.UpstreamError
	require.ErrorAs(t, inspectStream(failure), &upstream)
	require.Equal(t, 418, upstream.StatusCode)
}

func TestReadyStreamRetriesQueuedModelBeforeReturningContent(t *testing.T) {
	original := endpointsGlobal
	defer func() { endpointsGlobal = original }()
	var calls atomic.Int32
	frame := wrapEnvelope(t, `{"choices":[{"delta":{"role":"assistant","content":"OK"}}]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "userinfo") {
			_, _ = io.WriteString(w, `{"id":"u1"}`)
			return
		}
		attempt := calls.Add(1)
		if attempt == 1 {
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"isQueued":true,"retryAfterSeconds":1,"serviceAvailable":false}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+frame+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	endpointsGlobal.userinfo = server.URL + "/userinfo"
	endpointsGlobal.chat = server.URL + "/chat"
	acc := &reverse.Account{ID: 1, Region: "global", Credentials: map[string]any{"access_token": "dt-test-access"}}
	p := New().(*Provider)
	resp, err := p.Forward(context.Background(), acc, []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"hello"}]}`), false)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(raw), "OK")
	require.EqualValues(t, 2, calls.Load())
}

func TestReadyStreamQueueWaitHonorsCancellation(t *testing.T) {
	original := endpointsGlobal
	defer func() { endpointsGlobal = original }()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "userinfo") {
			_, _ = io.WriteString(w, `{"id":"u1"}`)
			return
		}
		calls.Add(1)
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"isQueued":true,"retryAfterSeconds":60,"serviceAvailable":false}`)
	}))
	defer server.Close()
	endpointsGlobal.userinfo = server.URL + "/userinfo"
	endpointsGlobal.chat = server.URL + "/chat"
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	acc := &reverse.Account{ID: 1, Region: "global", Credentials: map[string]any{"access_token": "dt-test-access"}}
	_, err := New().(*Provider).Forward(ctx, acc, []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"hello"}]}`), false)
	require.True(t, errors.Is(err, context.DeadlineExceeded), err)
	require.EqualValues(t, 1, calls.Load())
}

func TestQuotaNormalizesFreePlanAndAddonBuckets(t *testing.T) {
	original := endpointsGlobal
	defer func() { endpointsGlobal = original }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer dt-test-access", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/userinfo":
			_, _ = io.WriteString(w, `{"id":"u1"}`)
		case "/quota":
			_, _ = io.WriteString(w, `{"userQuota":{"used":0,"total":0,"remaining":0},"addOnQuota":{"used":1,"total":11,"remaining":10}}`)
		case "/plan":
			_, _ = io.WriteString(w, `{"plan_tier_name":"Free"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpointsGlobal.userinfo = server.URL + "/userinfo"
	endpointsGlobal.quota = server.URL + "/quota"
	endpointsGlobal.plan = server.URL + "/plan"
	result, err := New().(*Provider).Quota(context.Background(), &reverse.Account{ID: 1, Region: "global", Credentials: map[string]any{"access_token": "dt-test-access"}})
	require.NoError(t, err)
	require.Equal(t, "Free", result.Subscription.Plan)
	// 0/0 的套餐桶是噪音，不再占一整排 chip；带余额的拓展包排到第一。
	require.Len(t, result.Groups, 1)
	require.Equal(t, "个人拓展包", result.Groups[0].DisplayName)
	require.Len(t, result.Summary, 3)
	require.InDelta(t, 10.0/11.0, result.Groups[0].Buckets[0].RemainingFraction, 0.0001)
	require.Equal(t, "addon_quota_remaining", result.Summary[0].Key)
	require.Equal(t, 10.0, result.Summary[0].Value)
}

// TestQuotaReadsSharedResourcePackage 覆盖团队账号：套餐席位用尽（3000/3000）
// 后，真正可用的额度在顶层 orgResourcePackage 中，总额字段叫 cap。
//
// 旧实现只读 userQuota / addOnQuota，这个桶从未被解析，账号列表于是一直显示
// 「已用 3000 / 总计 3000」，而 Qoder CN 控制台里明明还有共享资源包可用。
func TestQuotaReadsSharedResourcePackage(t *testing.T) {
	original := endpointsCN
	defer func() { endpointsCN = original }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/userinfo":
			_, _ = io.WriteString(w, `{"id":"u1"}`)
		case "/quota":
			_, _ = io.WriteString(w, `{"userQuota":{"used":3000,"total":3000,"remaining":0},`+
				`"orgResourcePackage":{"used":0,"cap":2000,"remaining":2000,"percentage":0,"available":true}}`)
		case "/plan":
			_, _ = io.WriteString(w, `{"plan_tier_name":"Teams"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	endpointsCN.userinfo = server.URL + "/userinfo"
	endpointsCN.quota = server.URL + "/quota"
	endpointsCN.plan = server.URL + "/plan"
	result, err := New().(*Provider).Quota(context.Background(), &reverse.Account{ID: 1, Region: "cn", Credentials: map[string]any{"access_token": "dt-test-access"}})
	require.NoError(t, err)
	// 席位已用尽（剩余的 0），共享资源包还有余额：页头必须取后者，
	// 否则账号表永远停在「0 / 3000」，而账号其实还能用 2000。
	require.Len(t, result.Groups, 2)
	require.Equal(t, "共享资源包", result.Groups[0].DisplayName)
	require.Equal(t, "套餐额度", result.Groups[1].DisplayName)
	require.InDelta(t, 1.0, result.Groups[0].Buckets[0].RemainingFraction, 0.0001)
	require.Contains(t, result.Groups[0].Buckets[0].Description, "已用 0 / 总计 2000，剩余 2000")
	require.Equal(t, "shared_quota_remaining", result.Summary[0].Key)
	require.Equal(t, 2000.0, result.Summary[0].Value)
	metrics := map[string]float64{}
	for _, metric := range result.Summary {
		metrics[metric.Key] = metric.Value
	}
	require.Equal(t, 2000.0, metrics["shared_quota_remaining"])
	require.Equal(t, 2000.0, metrics["shared_quota_total"])
	require.Equal(t, 0.0, metrics["shared_quota_used"])
}

// TestQuotaSkipsUnavailableSharedPackage 覆盖过期 / 停用 / 已退还的资源包：
// 这类包的余额取不出来，不能作为可用额度展示。
func TestQuotaSkipsUnavailableSharedPackage(t *testing.T) {
	result := BuildQuotaResponse(&Quota{
		UserQuota: &QuotaBucket{Used: 3000, Total: 3000},
		Shared:    nil,
	})
	for _, metric := range result.Summary {
		require.NotContains(t, metric.Key, "shared_quota")
	}
	require.Len(t, result.Groups, 1)

	// available=false 时 extractSharedPackage 必须丢弃整个桶。
	require.Nil(t, extractSharedPackage(map[string]any{
		"orgResourcePackage": map[string]any{"cap": 2000.0, "remaining": 2000.0, "used": 0.0, "available": false},
	}))
}

func TestQuotaExpiryUsesUnixSeconds(t *testing.T) {
	for _, input := range []int64{1794240000, 1794240000000} {
		result := BuildQuotaResponse(&Quota{ExpiresAt: input})
		require.Equal(t, []reverse.QuotaMetric{{
			Key: "expires_at", Label: "额度到期时间", Value: 1794240000, Format: "number", Unit: "unix",
		}}, result.Summary)
	}
	require.Empty(t, BuildQuotaResponse(&Quota{}).Summary)
}

func TestQoderResponsesPreserveCacheAndReasoningTokenDetails(t *testing.T) {
	frame := wrapEnvelope(t, `{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}}}`)
	sse := "data: " + frame + "\n\ndata: [DONE]\n\n"
	state, err := consumeStream(strings.NewReader(sse))
	require.NoError(t, err)
	var completion map[string]any
	require.NoError(t, json.Unmarshal(buildCompletion(state, "id", 1, "test"), &completion))
	usage := completion["usage"].(map[string]any)
	require.EqualValues(t, 4, usage["prompt_tokens_details"].(map[string]any)["cached_tokens"])
	require.EqualValues(t, 2, usage["completion_tokens_details"].(map[string]any)["reasoning_tokens"])
	var stream bytes.Buffer
	require.NoError(t, pumpStream(context.Background(), io.NopCloser(strings.NewReader(sse)), &stream, "id", 1, "test", true))
	require.Contains(t, stream.String(), `"cached_tokens":4`)
	require.Contains(t, stream.String(), `"reasoning_tokens":2`)
}

func TestModelCatalogReadsNestedContextAndDefaultOutputLimits(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"assistant":[
		{"key":"qmodel","display_name":"Reasoner","enable":true,"is_reasoning":true,"price_factor":0,"context_config":{"small":{"token_count":32000},"large":{"token_count":200000,"is_default":true}}},
		{"key":"gmodel","display_name":"Chat","enable":true,"max_input_tokens":64000}
	]}`), &raw))
	catalog, err := parseModels(raw)
	require.NoError(t, err)
	models := catalog.Models
	require.Len(t, models, 2)
	require.EqualValues(t, 200000, models[0].ContextLength)
	require.EqualValues(t, 32768, models[0].MaxOutputTokens)
	require.True(t, models[0].SupportsReasoning)
	require.Equal(t, "0", models[0].Credits)
	require.EqualValues(t, 64000, models[1].ContextLength)
	require.EqualValues(t, 16384, models[1].MaxOutputTokens)
}

func TestCheckinAuthenticationFailureCanReachGatewayRefresh(t *testing.T) {
	original := endpointsGlobal
	defer func() { endpointsGlobal = original }()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"expired dt-expired"}`)
	}))
	defer server.Close()
	endpointsGlobal.sashBase = server.URL
	result, err := New().(*Provider).Checkin(context.Background(), &reverse.Account{Region: "global", Credentials: map[string]any{"access_token": "dt-expired"}})
	var upstream *reverse.UpstreamError
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
	require.False(t, result.Success)
	require.NotContains(t, err.Error(), "dt-expired")
	require.Equal(t, 1, calls, "authentication failure must not trigger extra statistics requests")
}

// TestQuotaKeepsOneBucketWhenEverythingIsExhausted 覆盖额度真用光的账号：
// 全 0 的桶平时是噪音会被过滤，但一个都不剩时页面只会显示「暂无额度数据」，
// 看不出「已经用完」，所以必须保留第一个。
func TestQuotaKeepsOneBucketWhenEverythingIsExhausted(t *testing.T) {
	result := BuildQuotaResponse(&Quota{
		UserQuota:  &QuotaBucket{Used: 3000, Total: 3000},
		AddonQuota: &QuotaBucket{},
	})

	require.Len(t, result.Groups, 1)
	require.Equal(t, "套餐额度", result.Groups[0].DisplayName)
	require.Equal(t, "user_quota_remaining", result.Summary[0].Key)
	require.Equal(t, 0.0, result.Summary[0].Value)
}

// TestQuotaSummaryAndGroupsShareOrder 锁住两条路径同序：页头数字取 Summary 里
// 第一个有容量的族，两者各排各的就会出现「页头 2000/3000、第一条却 0/3000」。
func TestQuotaSummaryAndGroupsShareOrder(t *testing.T) {
	result := BuildQuotaResponse(&Quota{
		UserQuota:  &QuotaBucket{Used: 3000, Total: 3000},
		AddonQuota: &QuotaBucket{Used: 3000, Total: 3000},
		Shared:     &QuotaBucket{Remaining: 2000, Total: 3000, Used: 1000},
	})

	require.Len(t, result.Groups, 3)
	require.Equal(t, []string{"shared_quota", "user_quota", "addon_quota"}, []string{
		strings.TrimSuffix(result.Summary[0].Key, "_remaining"),
		strings.TrimSuffix(result.Summary[3].Key, "_remaining"),
		strings.TrimSuffix(result.Summary[6].Key, "_remaining"),
	})
	require.Equal(t, "共享资源包", result.Groups[0].DisplayName)
	require.Equal(t, "套餐额度", result.Groups[1].DisplayName)
	require.Equal(t, "个人拓展包", result.Groups[2].DisplayName)
}

// TestModelCatalogReadsEveryCategory locks the fix for models hidden by the
// old "first non-empty category wins" rule: a model that only exists in a
// later upstream category must still reach the caller.
func TestModelCatalogReadsEveryCategory(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"assistant":[{"key":"amodel","display_name":"Alpha","enable":true}],
		"developer":[{"key":"qoduck","display_name":"QoDuck","enable":true}],
		"chat":[{"key":"cmodel","display_name":"Gamma","enable":true}]
	}`), &raw))

	catalog, err := parseModels(raw)
	require.NoError(t, err)

	ids := catalog.IDs()
	require.ElementsMatch(t, []string{"alpha", "qoduck", "gamma"}, ids)
	require.ElementsMatch(t, []string{"assistant", "developer", "chat"}, catalog.SelectedCategories)
	// Every configured category is counted, present or not, so an operator can
	// tell "the upstream sent no app models" from "we never looked".
	require.Equal(t, 1, catalog.Categories["assistant"])
	require.Equal(t, 1, catalog.Categories["developer"])
	require.Equal(t, 1, catalog.Categories["chat"])
	require.Equal(t, 0, catalog.Categories["app"])
	// Every parsed model is reachable by its public name.
	require.Equal(t, "qoduck", catalog.Resolution["qoduck"])
}

// TestModelCatalogReportsDroppedEntries locks that dropped catalogue entries are
// never silent: an operator asking "why is this model missing" gets a reason.
//
// enable=false is not a drop reason in the primary categories: those models are
// still listed and still callable, so they stay in the catalogue.
func TestModelCatalogReportsDroppedEntries(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"assistant":[
		{"key":"good","display_name":"Good","enable":true},
		{"key":"off","display_name":"Disabled","enable":false},
		{"display_name":"NoKey","enable":true},
		{"key":"good","display_name":"Good","enable":true}
	]}`), &raw))

	catalog, err := parseModels(raw)
	require.NoError(t, err)
	// The disabled entry stays: only duplicates and keyless entries are dropped.
	require.ElementsMatch(t, []string{"good", "disabled"}, catalog.IDs())

	reasons := map[string]string{}
	for _, skipped := range catalog.Skipped {
		reasons[skipped.Key] = skipped.Reason
	}
	require.Equal(t, reverse.SkipReasonMissingKey, reasons[""])
	require.Equal(t, reverse.SkipReasonDuplicate, reasons["good"])
	require.NotContains(t, reasons, "off")
}

// TestModelCatalogIncludesEnabledSecondaryModels locks the fix for models that
// exist only in a product-line projection: QoDuck (key=lmodel) is listed under
// "app" and nowhere else, so a catalogue built from the primary categories only
// could never route to it even though the vendor client offers it.
//
// It also locks the other half of the rule: the secondary lists carry
// scenario-only keys marked enable=false (quest-auto, nap-auto, …) which must
// not leak into the catalogue.
func TestModelCatalogIncludesEnabledSecondaryModels(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"assistant":[{"key":"gmodel","display_name":"General","enable":false}],
		"app":[
			{"key":"lmodel","display_name":"QoDuck","enable":true,"price_factor":0},
			{"key":"quest-auto","display_name":"Quest Auto","enable":false}
		],
		"qwork":[{"key":"qwork-ultimate","display_name":"QWork Ultimate","enable":false}],
		"nap":[{"key":"nmodel","display_name":"Nap Model","enable":true}]
	}`), &raw))

	catalog, err := parseModels(raw)
	require.NoError(t, err)

	// gmodel survives despite enable=false: the primary categories are read in
	// full. lmodel/nmodel come in from the secondary ones.
	require.ElementsMatch(t, []string{"general", "qoduck", "nap-model"}, catalog.IDs())
	require.ElementsMatch(t, []string{"assistant", "app", "qwork", "nap"}, catalog.SelectedCategories)

	// The catalogue can route to QoDuck by its public name.
	require.Equal(t, "lmodel", catalog.Resolution["qoduck"])

	// Scenario-only keys are reported, not silently dropped, and explain why
	// they are absent: they belong to a projection the provider does not serve.
	reasons := map[string]string{}
	for _, skipped := range catalog.Skipped {
		reasons[skipped.Key] = skipped.Reason
	}
	require.Equal(t, reverse.SkipReasonOtherCategory, reasons["quest-auto"])
	require.Equal(t, reverse.SkipReasonOtherCategory, reasons["qwork-ultimate"])
	require.NotContains(t, reasons, "lmodel")
}

// TestModelCatalogDeduplicatesAcrossCategories locks that reading every
// category does not duplicate a model listed twice, and that the duplicate is
// reported rather than silently merged.
//
// The primary projection wins: when a key appears in both, the primary entry is
// the one kept and the secondary occurrence reported as a duplicate.
func TestModelCatalogDeduplicatesAcrossCategories(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"assistant":[{"key":"amodel","display_name":"Alpha","enable":true}],
		"developer":[{"key":"amodel","display_name":"Alpha","enable":true}]
	}`), &raw))

	catalog, err := parseModels(raw)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha"}, catalog.IDs())
	require.Len(t, catalog.Skipped, 1)
	require.Equal(t, reverse.SkipReasonDuplicate, catalog.Skipped[0].Reason)
}

// TestResolveModelReportsPassthrough locks the signal that distinguishes a real
// model from a typo. The upstream answers unknown names with a default model,
// so passthrough is the only observable difference.
func TestResolveModelReportsPassthrough(t *testing.T) {
	provider := New().(*Provider)
	acc := &reverse.Account{ID: 7, Region: "global", Credentials: map[string]any{}}
	provider.models[acc.ID] = &modelEntry{
		models:      []reverse.Model{{ID: "qoduck"}},
		resolution:  map[string]string{"qoduck": "qoduck_key"},
		fingerprint: credentialFingerprint(acc),
		fetchedAt:   time.Now(),
	}

	if key, source := provider.resolveModelWithSource(acc, "qoduck"); key != "qoduck_key" || source != reverse.ModelSourceCatalog {
		t.Fatalf("catalog hit = (%q, %q)", key, source)
	}
	if _, source := provider.resolveModelWithSource(acc, "claude-sonnet-4-6"); source != reverse.ModelSourceFamily {
		t.Fatalf("family alias source = %q", source)
	}
	if _, source := provider.resolveModelWithSource(acc, "zzz-not-a-real-model"); source != reverse.ModelSourcePassthrough {
		t.Fatalf("unknown model source = %q", source)
	}
}
