package qoder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/reverse"
)

type replayBody struct {
	io.Reader
	io.Closer
}

// Inspect the first meaningful frame before exposing a 200 response. This lets
// both streaming and aggregated requests recover from early envelope failures
// without replaying any content that a client has already received.
func inspectStream(resp *http.Response) error {
	reader := bufio.NewReaderSize(resp.Body, 64*1024)
	var prefix bytes.Buffer
	var pending bytes.Buffer
	for {
		chunk, err := reader.ReadSlice('\n')
		pending.Write(chunk)
		prefix.Write(chunk)
		if prefix.Len() > 8*1024*1024 {
			return reverse.NewUpstreamError(502, "upstream_frame_too_large", "qoder initial stream frame exceeds 8 MiB")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		line := pending.String()
		pending.Reset()
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(text, "data:"))
			if payload == "[DONE]" {
				return reverse.NewUpstreamError(502, "empty_upstream_stream", "upstream returned an empty stream")
			}
			if signal, queued := ParseQueueSignal(payload); queued {
				return queueSignalError(signal)
			}
			delta := extractDelta(payload)
			if delta.Err != nil {
				return delta.Err
			}
			if !delta.empty() {
				resp.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(prefix.Bytes()), reader), Closer: resp.Body}
				return nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return reverse.NewUpstreamError(502, "empty_upstream_stream", "upstream returned an empty stream")
			}
			return err
		}
	}
}

func queueSignalError(signal QueueSignal) *reverse.UpstreamError {
	return &reverse.UpstreamError{StatusCode: 503, Code: "upstream_model_busy", Message: "上游模型排队中，请稍后重试", RetryAfter: signal.WaitDuration(), Details: map[string]any{"queue": signal}}
}

func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var upstream *reverse.UpstreamError
	if errors.As(err, &upstream) {
		if upstream.Code == "empty_upstream_stream" || upstream.StatusCode == http.StatusUnauthorized ||
			upstream.StatusCode == http.StatusForbidden || upstream.StatusCode == http.StatusTooManyRequests ||
			upstream.Kind == string(reverse.FailureKindClient) || upstream.Kind == string(reverse.FailureKindContentBlocked) {
			return false
		}
		for _, marker := range []string{"invalid_parameter_error", "invalid_request_error", "authentication_error", "permission_error", `"Range of `, "DataInspectionFailed", "inappropriate content", "input text data may contain", "ContentFilter", "SensitiveContent"} {
			if strings.Contains(upstream.Code+" "+upstream.Message, marker) {
				return false
			}
		}
		return upstream.StatusCode == 418 || upstream.StatusCode >= 500 || (upstream.StatusCode >= 400 && strings.Contains(upstream.Message, "provider_error"))
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"x509:", "invalid url", "unsupported protocol", "unknown scheme"} {
		if strings.Contains(message, marker) {
			return false
		}
	}
	for _, marker := range []string{"ssl", "eof", "reset", "timed out", "timeout", "broken pipe", "connection"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (p *Provider) openReadyChatStream(ctx context.Context, acc *reverse.Account, br *bridge, envelope map[string]any, model string) (*http.Response, error) {
	transientRetries, queueWaits := 0, 0
	for {
		resp, err := p.openChatStream(ctx, acc, br, envelope, model)
		if err == nil {
			err = inspectStream(resp)
			if err == nil {
				return resp, nil
			}
			_ = resp.Body.Close()
		}
		var upstream *reverse.UpstreamError
		wait := time.Duration(0)
		if errors.As(err, &upstream) && upstream.Code == "upstream_model_busy" {
			if queueWaits >= DefaultQueueMaxWaits {
				return nil, err
			}
			queueWaits++
			wait = upstream.RetryAfter
		} else {
			if transientRetries >= 2 || !retryable(err) {
				return nil, err
			}
			transientRetries++
			wait = time.Duration(transientRetries) * time.Second
		}
		if !reverse.SleepContext(ctx, wait) {
			return nil, ctx.Err()
		}
	}
}
