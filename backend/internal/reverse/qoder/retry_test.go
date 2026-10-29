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
	require.Len(t, result.Groups, 2)
	require.Len(t, result.Summary, 6)
	require.Equal(t, 0.0, result.Groups[0].Buckets[0].RemainingFraction)
	require.InDelta(t, 10.0/11.0, result.Groups[1].Buckets[0].RemainingFraction, 0.0001)
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
	var catalog map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"assistant":[
		{"key":"qmodel","display_name":"Reasoner","enable":true,"is_reasoning":true,"price_factor":0,"context_config":{"small":{"token_count":32000},"large":{"token_count":200000,"is_default":true}}},
		{"key":"gmodel","display_name":"Chat","enable":true,"max_input_tokens":64000}
	]}`), &catalog))
	models, _ := parseModels(catalog)
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
