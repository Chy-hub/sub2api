package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUSTCSpeedProbeRestrictsPortsAndResponsesParameters(t *testing.T) {
	for _, tc := range []struct {
		base    string
		allowed bool
	}{
		{"https://api.llm.ustc.edu.cn", true}, {"https://api.llm.ustc.edu.cn:443/v1", true},
		{"https://api.llm.ustc.edu.cn:8443", false}, {"http://api.llm.ustc.edu.cn", false},
		{"https://api.llm.ustc.edu.cn.evil.example", false},
	} {
		t.Run(tc.base, func(t *testing.T) {
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": tc.base}}
			_, err := speedProbeURL(account, "responses")
			require.Equal(t, tc.allowed, err == nil)
		})
	}
	task := DefaultUSTCSpeedTask()
	seed, topP := int64(3), 0.9
	task.Seed, task.TopP, task.Stop = &seed, &topP, []string{"END"}
	for _, mode := range []string{"responses", "chat_completions"} {
		task.APIMode = mode
		body, err := speedProbeBody(task, "model", "prompt")
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(body, &fields))
		_, stop := fields["stop"]
		_, seed := fields["seed"]
		require.Equal(t, mode != "responses", stop)
		require.Equal(t, mode != "responses", seed)
		require.Equal(t, 0.9, fields["top_p"])
	}
}

func TestUSTCSpeedLocalAdmissionCannotBeSpoofedByUpstreamHeader(t *testing.T) {
	for _, local := range []bool{false, true} {
		var body io.ReadCloser = io.NopCloser(strings.NewReader("{}"))
		if local {
			body = &ustcLocalResponseBody{body}
		}
		watch := &ustcSpeedWatch{}
		watch.headers(&http.Response{StatusCode: 200, Header: http.Header{"X-Sub2api-Ustc-Local-Admission": []string{"1"}}, Body: body})
		require.Equal(t, local, watch.local)
	}
}

func TestUSTCSpeedCollectorParsesBareJSONLinesBeforeSSEFields(t *testing.T) {
	start := time.Now()
	collector := NewUSTCSpeedCollector(start, true)
	collector.Headers(start, 200, "text/event-stream")
	collector.Observe([]byte("{\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n"), start.Add(100*time.Millisecond))
	collector.Observe([]byte("{\"choices\":[{\"delta\":{\"content\":\"b\"}}],\"usage\":{\"completion_tokens\":2}}\n"), start.Add(200*time.Millisecond))
	collector.Observe([]byte("data: [DONE]\n\n"), start.Add(250*time.Millisecond))
	obs := collector.Finish(start.Add(300*time.Millisecond), nil)
	require.Equal(t, 2, obs.EventCount)
	require.Equal(t, 2, obs.OutputChars)
	require.Equal(t, 2, obs.OutputTokens)
	require.NotNil(t, obs.GenerationTPS)
	require.InDelta(t, 10, *obs.GenerationTPS, 1e-9)
	require.True(t, obs.StreamDone)
}
