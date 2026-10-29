package workbuddy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func localProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	client := server.Client()
	transport := client.Transport
	client.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		copied := *req.URL
		clone.URL = &copied
		clone.Host = req.URL.Host
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		return transport.RoundTrip(clone)
	})
	p := New().(*Provider)
	p.clientFactory = func(*reverse.Account) *http.Client { return client }
	return p
}

func TestForwardSanitizerPerAccountKeepsImagesAndValidToolJSON(t *testing.T) {
	const template = "You are Claude Code, Anthropic's official CLI for Claude"
	received := make(chan map[string]any, 2)
	headers := make(chan http.Header, 2)
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil {
			t.Error("invalid request JSON")
		}
		received <- payload
		headers <- r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"id":"c1","object":"chat.completion.chunk","model":"test","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
	})
	args, _ := json.Marshal(map[string]any{"prompt": "x-anthropic-billing-header: secret", "nested": []any{template}, "large": json.Number("9007199254740993")})
	image := "data:image/png;base64,aGVsbG8="
	input := map[string]any{"model": "test", "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "Write", "parameters": map[string]any{"type": "object"}}}}, "messages": []any{
		map[string]any{"role": "system", "content": template},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hello"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": image}}}},
		map[string]any{"role": "assistant", "content": nil, "reasoning_content": template, "tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]any{"name": "Write", "arguments": string(args)}}}},
		map[string]any{"role": "tool", "tool_call_id": "call-1", "content": "done"},
	}}
	body, _ := json.Marshal(input)
	input["stream_options"] = map[string]any{"include_usage": false}
	body, _ = json.Marshal(input)
	original := bytes.Clone(body)
	for _, sanitize := range []bool{true, false} {
		acc := &reverse.Account{ID: 1, Region: "cn", Credentials: map[string]any{"access_token": "account-access", "uid": "uid-a"}, Extra: map[string]any{"reverse_sanitize_fingerprints": sanitize, "reverse_passthrough_ip": sanitize}}
		resp, err := p.Forward(reverse.WithClientIP(context.Background(), "192.0.2.10"), acc, body, false)
		require.NoError(t, err)
		_ = resp.Body.Close()
		got := <-received
		require.Equal(t, true, got["stream_options"].(map[string]any)["include_usage"])
		outHeaders := <-headers
		messages := got["messages"].([]any)
		first := messages[0].(map[string]any)["content"].(string)
		if sanitize {
			require.NotEqual(t, template, first)
			require.Equal(t, "192.0.2.10", outHeaders.Get("X-Forwarded-For"))
		} else {
			require.Equal(t, template, first)
			require.Empty(t, outHeaders.Get("X-Forwarded-For"))
		}
		require.Equal(t, "Bearer account-access", outHeaders.Get("Authorization"))
		require.Empty(t, outHeaders.Get("X-Refresh-Token"))
		require.Equal(t, image, messages[1].(map[string]any)["content"].([]any)[1].(map[string]any)["image_url"].(map[string]any)["url"])
		call := messages[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
		function := call["function"].(map[string]any)
		require.Equal(t, "Write", function["name"])
		arguments := function["arguments"].(string)
		require.True(t, json.Valid([]byte(arguments)), arguments)
		require.Contains(t, arguments, "9007199254740993")
		if sanitize {
			require.NotContains(t, arguments, "x-anthropic-billing-header:")
			require.NotContains(t, arguments, template)
		} else {
			require.Equal(t, string(args), arguments)
		}
	}
	require.Equal(t, original, body)
}

func TestCheckinAndTaskClientsUseAccountIdentity(t *testing.T) {
	const token = "device-token-a"
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "machine-a", r.Header.Get("X-Machine-ID"))
		require.Equal(t, "session-a", r.Header.Get("X-Session-ID"))
		require.Equal(t, token, r.Header.Get("X-Device-Token"))
		require.Equal(t, "WorkBuddy/7.1", r.Header.Get("User-Agent"))
		_, _ = io.WriteString(w, `{"code":0,"data":{"credit":5,"energy":2,"streak":3}}`)
	})
	acc := &reverse.Account{Region: "cn", Credentials: map[string]any{"uid": "u1", "access_token": "access", "device_token": token, "machine_id": "machine-a", "session_id": "session-a", "client_version": "7.1"}}
	result, err := p.Checkin(context.Background(), acc)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "5 credits", result.Reward)
	acc.Credentials["enterprise_id"] = "enterprise"
	result, err = p.Checkin(context.Background(), acc)
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "unsupported", result.Details["status"])
}

func TestTaskRewardClaimUsesWebDomainAndRejectsMalformedSuccess(t *testing.T) {
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "www.workbuddy.cn", r.Host)
		require.Equal(t, "/activity/growth/tasks/chat_5/claim", r.URL.Path)
		require.Equal(t, "web", r.Header.Get("X-Client-Platform"))
		require.Equal(t, "Bearer access", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	})
	acc := &reverse.Account{Region: "cn", Credentials: map[string]any{"access_token": "access"}}
	_, _, err := p.tasks(acc).client(context.Background()).ClaimReward(context.Background(), newCredential(acc), "chat_5")
	require.ErrorContains(t, err, "missing reward fields")
}

func TestGlobalCheckinUsesRegionalBeaconWithoutClaimingImmediateCredits(t *testing.T) {
	report := 0
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "www.workbuddy.ai", r.Host)
		if strings.HasSuffix(r.URL.Path, "heatmap") {
			_, _ = io.WriteString(w, `{"code":0,"data":{"cells":[{"date":"2026-10-29","score":0}]}}`)
			return
		}
		require.Equal(t, "/v2/report", r.URL.Path)
		report++
		var events []map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&events))
		require.Equal(t, "u-global", events[0]["userId"])
		_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
	})
	acc := &reverse.Account{Region: "global", Credentials: map[string]any{"uid": "u-global", "access_token": "access"}}
	result, err := p.Checkin(context.Background(), acc)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Empty(t, result.Reward)
	require.Contains(t, result.Message, "next day")
	require.Equal(t, 1, report)
}

func TestTaskPacingCancelsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	require.False(t, reverse.SleepContext(ctx, 10*time.Minute))
	require.Less(t, time.Since(started), time.Second)
}

func TestTravelDoesNotHideUpstreamFailure(t *testing.T) {
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, buddyInfoPath, r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"message":"travel temporarily unavailable"}`)
	})
	_, err := p.RunScheduled(context.Background(), &reverse.Account{Region: "cn"}, TaskKindTravel)
	require.ErrorContains(t, err, "travel temporarily unavailable")
}

func TestSchoolClaimsCompletedTaskWithoutGeneratingAnotherChatAndKeepsPartialErrors(t *testing.T) {
	claims := 0
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case schoolBasePath + "/tasks":
			_, _ = io.WriteString(w, `{"code":0,"data":{"in_period":true,"tasks":[{"task_code":"desktop_chat_1_time","status":"completed"},{"task_code":"share_invite","status":"completed"}]}}`)
		case schoolBasePath + "/tasks/desktop_chat_1_time/claim":
			claims++
			_, _ = io.WriteString(w, `{"code":0,"data":{"chance_granted":0}}`)
		case schoolBasePath + "/tasks/share_invite/claim":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"message":"claim unavailable"}`)
		case schoolBasePath + "/config":
			_, _ = io.WriteString(w, `{"code":0,"data":{"chance":{"balance":0}}}`)
		default:
			t.Errorf("completed tasks must not replay chat or event steps: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	message, err := p.RunScheduled(context.Background(), &reverse.Account{Region: "cn"}, TaskKindSchool)
	require.ErrorContains(t, err, "claim unavailable")
	require.Contains(t, message, "完成 1 个任务")
	require.Equal(t, 1, claims)
}

func TestLockedGrowthTaskIsPendingAndDoesNotProduceEvents(t *testing.T) {
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, growthTasksPath, r.URL.Path)
		_, _ = io.WriteString(w, `{"code":0,"data":{"tasks":[{"task_code":"chat_5","locked":true}]}}`)
	})
	result, err := p.RunTask(context.Background(), &reverse.Account{Region: "cn"}, "chat_5")
	require.NoError(t, err)
	require.Equal(t, "pending", result.Status)
}

func TestDesktopTaskChatDoesNotIgnoreSSEErrorWithoutRequestIDRequirement(t *testing.T) {
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "data: "+`{"error":{"code":"11101","message":"unmarshal chat params failed"}}`+"\n\n")
	})
	acc := &reverse.Account{Region: "cn"}
	err := p.tasks(acc).client(context.Background()).DesktopChat(context.Background(), newCredential(acc), "", "glm-5.2", "GLM-5.2", "conversation")
	require.ErrorContains(t, err, "11101")
}

func TestNightWindowUsesUpstreamTimezone(t *testing.T) {
	nightUTC := time.Date(2026, 10, 29, 15, 30, 0, 0, time.UTC)
	dayUTC := time.Date(2026, 10, 30, 0, 30, 0, 0, time.UTC)
	require.True(t, InNightWindow(nightUTC))
	require.False(t, InNightWindow(dayUTC))
}

func TestAlreadyExpiredSessionIsNotMistakenForSuccessfulCheckin(t *testing.T) {
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"session already expired"}`)
	})
	result, err := p.Checkin(context.Background(), &reverse.Account{Region: "cn"})
	require.ErrorContains(t, err, "already expired")
	require.Nil(t, result)
	require.True(t, IsAlreadyCheckin(errors.New("already checked in today")))
	require.True(t, IsAlreadyCheckin(errors.New("今日已签到")))
}

func TestAPICallRejectsOversizedAndRedirectResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		limit  int64
	}{
		{"oversized", 200, `{"code":0,"data":{}}` + strings.Repeat(" ", 100), 64},
		{"redirect", 302, `{"code":0,"data":{}}`, 64},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			req, err := http.NewRequest(http.MethodGet, server.URL, nil)
			require.NoError(t, err)
			_, err = apiCall(server.Client(), req, test.limit)
			require.Error(t, err)
		})
	}
}

func TestModelFallbackPreservesSeedContextAndReasoning(t *testing.T) {
	models := seedModels(regionCN)
	require.NotEmpty(t, models)
	withContext, withReasoning := false, false
	for _, model := range models {
		withContext = withContext || (model.ContextLength > 0 && model.MaxOutputTokens > 0)
		withReasoning = withReasoning || (model.SupportsReasoning && len(model.Efforts) > 0)
	}
	require.True(t, withContext)
	require.True(t, withReasoning)
}

func TestStreakIgnoresClaimedRewardsButReportsLotteryFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case growthHeatmapPath:
					_, _ = io.WriteString(w, `{"code":0,"data":{"cells":[]}}`)
				case claimGiftPath, claimCompPath:
					_, _ = io.WriteString(w, `{"code":10001,"msg":"reward already claimed"}`)
				case growthStreakPath:
					_, _ = io.WriteString(w, `{"code":0,"data":{"streak":{"days":1}}}`)
				case growthLotterySummary:
					if fail {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"message":"lottery unavailable"}`)
					} else {
						_, _ = io.WriteString(w, `{"code":0,"data":{"chances":0}}`)
					}
				default:
					t.Errorf("unexpected streak request: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			_, err := p.RunScheduled(context.Background(), &reverse.Account{Region: "cn"}, TaskKindStreak)
			if fail {
				require.ErrorContains(t, err, "lottery unavailable")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestModelDiscoveryMergesIDEAndCLIProbes(t *testing.T) {
	var ideCalls, cliCalls int32
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"enterprise-model","name":"Enterprise","supportsToolCall":true}],"agents":[{"name":"cli","models":["enterprise-model"]}]}}`)
			return
		}
		if strings.HasPrefix(r.Header.Get("User-Agent"), "CodeBuddyIDE/") {
			atomic.AddInt32(&ideCalls, 1)
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"ide-model","name":"IDE","maxInputTokens":200000,"maxOutputTokens":10000,"credits":"x0.05"}]}}`)
		} else {
			atomic.AddInt32(&cliCalls, 1)
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"cli-model","name":"CLI","reasoning":{"supportedEfforts":["low","high"],"defaultEffort":"high"}}]}}`)
		}
	})
	acc := &reverse.Account{ID: 1, Region: "cn", Credentials: map[string]any{"access_token": "access", "uid": "u1"}}
	models, err := p.ListModels(context.Background(), acc)
	require.NoError(t, err)
	require.Len(t, models, 3)
	require.Equal(t, "ide-model", models[0].ID)
	require.Equal(t, "x0.05", models[0].Credits)
	require.EqualValues(t, 200000, models[0].ContextLength)
	require.Equal(t, []string{"low", "high"}, models[1].Efforts)
	_, err = p.ListModels(context.Background(), acc)
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&ideCalls))
	require.EqualValues(t, 1, atomic.LoadInt32(&cliCalls))
	acc.Credentials["access_token"] = "rotated"
	_, err = p.ListModels(context.Background(), acc)
	require.NoError(t, err)
	require.EqualValues(t, 2, atomic.LoadInt32(&ideCalls))
}

func TestPromptDegradationAndReasoningCapabilitiesAreAccountScoped(t *testing.T) {
	const customPrompt = "Original custom prompt"
	var calls int
	p := localProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"code":11101,"msg":"blocked by security policy"}`)
			return
		}
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		first := payload["messages"].([]any)[0].(map[string]any)["content"]
		if calls == 2 {
			require.NotEqual(t, customPrompt, first)
		} else {
			require.Equal(t, customPrompt, first)
		}
		_, _ = io.WriteString(w, "data: "+`{"id":"id","choices":[{"index":0,"delta":{"content":"OK"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	})
	a := &reverse.Account{ID: 1, Region: "cn", Credentials: map[string]any{"access_token": "account-a", "uid": "uid"}, Extra: map[string]any{"reverse_prompt_mode": "custom", "reverse_prompt_text": customPrompt}}
	b := &reverse.Account{ID: 2, Region: "cn", Credentials: map[string]any{"access_token": "account-b", "uid": "uid"}, Extra: map[string]any{"reverse_prompt_mode": "custom", "reverse_prompt_text": customPrompt}}
	p.publishEfforts(catalogKey(a), map[string]modelEfforts{"model": {supported: []string{"high"}, fallback: "high"}})
	_, found := p.effortEntry(b, "model")
	require.False(t, found)
	body := []byte(`{"model":"model","messages":[{"role":"user","content":"hello"}]}`)
	_, err := p.Forward(context.Background(), a, body, false)
	require.Error(t, err)
	for _, acc := range []*reverse.Account{a, b} {
		resp, err := p.Forward(context.Background(), acc, body, false)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
}
