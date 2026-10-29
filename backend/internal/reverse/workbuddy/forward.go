package workbuddy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

// promptSettings resolves the per-account prompt mode/text. A custom or append
// mode without text falls back to the embedded default prompt.
func promptSettings(acc *reverse.Account) (string, string) {
	mode := strings.ToLower(acc.ExtraString("reverse_prompt_mode"))
	text := acc.ExtraString("reverse_prompt_text")
	if mode != "custom" && mode != "append" {
		return "passthrough", ""
	}
	if text == "" {
		text = defaultPromptText()
	}
	return mode, text
}

// effortEntry returns the cached reasoning capability of a model. An absent
// entry means "unknown": efforts then pass through untouched.
func (p *Provider) effortEntry(acc *reverse.Account, model string) (modelEfforts, bool) {
	if p == nil {
		return modelEfforts{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	table := p.efforts[catalogKey(acc)]
	entry, okEntry := table[strings.TrimSpace(model)]
	return entry, okEntry
}

// Forward sends a Chat Completions request upstream and returns either a
// normalized SSE stream (stream=true) or an aggregated chat.completion JSON
// body (stream=false).
func (p *Provider) Forward(ctx context.Context, acc *reverse.Account, ccBody []byte, stream bool) (*http.Response, error) {
	if acc == nil {
		return nil, errors.New("workbuddy: account is required")
	}
	cred := newCredential(acc)
	model := requestModel(ccBody)
	if model == "" {
		return nil, errors.New("workbuddy: request body is missing a model")
	}

	conversation := conversationKey(ccBody)
	meta := chatMeta{
		conversationID:        conversation,
		conversationRequestID: newUUIDv7Hex(),
	}
	cacheKey := buildCacheKey(cred.uid, firstNonEmpty(conversation, meta.conversationRequestID))
	mode, promptText := promptSettings(acc)
	promptKey := fmt.Sprintf("%s|%x", catalogKey(acc), sha256.Sum256([]byte(mode+"|"+promptText)))
	p.mu.Lock()
	degraded := p.degraded[acc.ID] == promptKey
	p.mu.Unlock()
	if degraded && mode != "passthrough" {
		promptText = "You are a helpful assistant. Respond in the user's language, follow the user's instructions, and be direct and concise."
	}

	opts := prepareOptions{
		sanitize:   acc.Extra["reverse_sanitize_fingerprints"] != false,
		model:      model,
		promptMode: mode,
		promptText: promptText,
		cacheKey:   cacheKey,
		global:     cred.region.isGlobal(),
	}
	if entry, okEntry := p.effortEntry(acc, model); okEntry {
		if entry.fallback != "" {
			opts.defaultEfforts = map[string]string{model: entry.fallback}
		}
		if len(entry.supported) > 0 {
			opts.supportedEfforts = map[string][]string{model: entry.supported}
		}
	} else if entry, okSeed := seedEffortFor(cred.region, model); okSeed {
		// No catalog probe has run yet (or the model is missing from it); use
		// the embedded per-region table so efforts are still normalized.
		if entry.fallback != "" {
			opts.defaultEfforts = map[string]string{model: entry.fallback}
		}
		if len(entry.supported) > 0 {
			opts.supportedEfforts = map[string][]string{model: entry.supported}
		}
	}
	prepared := prepareBody(ccBody, opts)

	endpoint := endpointsFor(cred.region).chatBase + chatCompletionsPath
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(prepared))
	if errReq != nil {
		return nil, fmt.Errorf("workbuddy build chat request: %w", errReq)
	}
	applyChatHeaders(req, cred, meta)
	if acc.Extra["reverse_passthrough_ip"] == true {
		if ip := reverse.ClientIP(ctx); ip != "" {
			req.Header.Set("X-Forwarded-For", ip)
			req.Header.Set("X-Real-IP", ip)
		}
	}

	resp, errDo := p.client(acc).Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("workbuddy chat request: %w", errDo)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, apiBodyLimit))
		_ = resp.Body.Close()
		failure := upstreamError(resp.StatusCode, string(body), resp.Header)
		if failure.Kind == string(reverse.FailureKindContentBlocked) && mode != "passthrough" {
			p.mu.Lock()
			if p.degraded == nil {
				p.degraded = map[int64]string{}
			}
			p.degraded[acc.ID] = promptKey
			p.mu.Unlock()
		}
		return nil, failure
	}

	if !stream {
		defer func() { _ = resp.Body.Close() }()
		aggregated, errAggregate := aggregateChatCompletion(resp.Body)
		if errAggregate != nil {
			return nil, errAggregate
		}
		response := jsonResponse(aggregated)
		reverse.CopyCorrelationHeaders(response.Header, resp.Header)
		return response, nil
	}

	response := streamResponse(ctx, resp)
	reverse.CopyCorrelationHeaders(response.Header, resp.Header)
	return response, nil
}

// jsonResponse wraps an aggregated body as a 200 JSON http.Response.
func jsonResponse(body []byte) *http.Response {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// streamResponse pipes a normalized SSE stream. The upstream body is closed by
// the producer goroutine; closing the returned body stops the producer.
func streamResponse(ctx context.Context, upstream *http.Response) *http.Response {
	reader, writer := io.Pipe()
	header := http.Header{}
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")

	go func() {
		defer func() { _ = upstream.Body.Close() }()
		errStream := streamChatCompletion(ctx, upstream.Body, writer)
		_ = writer.CloseWithError(errStream)
	}()

	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     header,
		Body:       reader,
	}
}

// Test probes an account with a short non-stream prompt.
func (p *Provider) Test(ctx context.Context, acc *reverse.Account, model, prompt string) (*reverse.TestResult, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("workbuddy: test model is required")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "Reply with OK."
	}
	body, errMarshal := json.Marshal(map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": 512,
		"messages": []any{
			map[string]any{"role": "user", "content": prompt},
		},
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("workbuddy build test request: %w", errMarshal)
	}

	resp, errForward := p.Forward(ctx, acc, body, false)
	if errForward != nil {
		return nil, errForward
	}
	defer func() { _ = resp.Body.Close() }()
	raw, errRead := io.ReadAll(io.LimitReader(resp.Body, apiBodyLimit))
	if errRead != nil {
		return nil, fmt.Errorf("workbuddy read test response: %w", errRead)
	}

	var completion struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, fmt.Errorf("workbuddy decode test response: %w", errUnmarshal)
	}
	content := ""
	if len(completion.Choices) > 0 {
		content = strings.TrimSpace(completion.Choices[0].Message.Content)
		if content == "" {
			content = strings.TrimSpace(completion.Choices[0].Message.ReasoningContent)
		}
	}
	return &reverse.TestResult{Content: content}, nil
}
