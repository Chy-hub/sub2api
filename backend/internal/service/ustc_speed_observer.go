package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ustcSpeedProbeKey struct{}
type ustcSpeedProbeContext struct {
	sample      USTCSpeedSample
	result      chan USTCSpeedSample
	first       chan struct{}
	firstOnce   sync.Once
	guard       func() bool
	blockReason string
}

type ustcSpeedWatch struct {
	request     *http.Request
	sample      USTCSpeedSample
	probe       *ustcSpeedProbeContext
	collector   *USTCSpeedCollector
	start       time.Time
	headersAt   time.Time
	httpStatus  int
	contentType string
	local       bool
	once        sync.Once
}

// beginUSTCSpeed attaches event-level timing only to scheduled probes. Real
// traffic is analyzed from existing usage records, so observing it here would
// duplicate data and add latency without new comparable metrics.
func (s *OpenAIGatewayService) beginUSTCSpeed(request *http.Request, account *Account) *ustcSpeedWatch {
	if s.ustcSpeed == nil {
		return nil
	}
	probe, _ := request.Context().Value(ustcSpeedProbeKey{}).(*ustcSpeedProbeContext)
	if probe == nil {
		return nil
	}
	return &ustcSpeedWatch{request: request, probe: probe, sample: probe.sample, start: time.Now()}
}
func (w *ustcSpeedWatch) sent() {
	if w == nil {
		return
	}
	w.start = time.Now()
	w.sample.StartedAt = w.start
	w.collector = NewUSTCSpeedCollector(w.start, w.sample.Stream)
}
func (w *ustcSpeedWatch) headers(response *http.Response) {
	if w == nil || response == nil {
		return
	}
	w.headersAt = time.Now()
	w.httpStatus = response.StatusCode
	w.contentType = response.Header.Get("Content-Type")
	_, w.local = response.Body.(*ustcLocalResponseBody)
}
func (w *ustcSpeedWatch) wrap(response *http.Response, err error) (*http.Response, error) {
	if w == nil {
		return response, err
	}
	if w.collector == nil {
		w.sent()
	}
	if response == nil || err != nil {
		w.finish(time.Now(), err)
		return response, err
	}
	if w.headersAt.IsZero() {
		w.headers(response)
	}
	// Observe the actual transport, including an upstream that returns a different
	// content type than the requested protocol. No bytes are consumed here.
	streaming := strings.Contains(strings.ToLower(w.contentType), "text/event-stream")
	if streaming != w.sample.Stream {
		w.sample.Stream = streaming
		w.collector = NewUSTCSpeedCollector(w.start, streaming)
	}
	w.collector.Headers(w.headersAt, w.httpStatus, w.contentType)
	if response.Body == nil {
		w.finish(time.Now(), io.EOF)
		return response, nil
	}
	response.Body = &ustcSpeedBody{ReadCloser: response.Body, watch: w}
	return response, nil
}

type ustcSpeedBody struct {
	io.ReadCloser
	watch *ustcSpeedWatch
}

func (b *ustcSpeedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	at := time.Now()
	if n > 0 {
		b.watch.collector.Observe(data[:n], at)
		if p := b.watch.probe; p != nil {
			select {
			case <-b.watch.collector.FirstOutput():
				p.firstOnce.Do(func() { close(p.first) })
			default:
			}
		}
	}
	if err != nil {
		b.watch.finish(at, err)
	}
	return n, err
}
func (b *ustcSpeedBody) Close() error {
	at := time.Now()
	err := b.ReadCloser.Close()
	finishErr := err
	if finishErr == nil {
		// A protocol consumer may stop immediately after a terminal frame
		// without performing another Read to receive EOF. StreamDone, rather
		// than a synthetic I/O error, determines whether that stream completed.
		finishErr = io.EOF
	}
	b.watch.finish(at, finishErr)
	return err
}
func (w *ustcSpeedWatch) finish(at time.Time, readErr error) {
	w.once.Do(func() {
		if w.collector == nil {
			w.sent()
		}
		obs := w.collector.Finish(at, readErr)
		sample := w.sample
		sample.USTCSpeedObservation = obs
		sample.HTTPStatus = w.httpStatus
		sample.FinishedAt = at
		switch {
		case w.local:
			sample.Status = "skipped"
			sample.Reason = "local_capacity"
			if w.probe != nil && w.probe.blockReason != "" {
				sample.Reason = w.probe.blockReason
			}
		case sample.HTTPStatus == 429:
			sample.Status = "rate_limited"
			sample.Reason = "upstream_429"
		case sample.HTTPStatus < 200 || sample.HTTPStatus >= 300:
			sample.Status = "error"
			sample.Reason = "upstream_http_error"
		case errors.Is(w.request.Context().Err(), context.Canceled) && !obs.StreamDone:
			sample.Status = "cancelled"
			sample.Reason = "request_cancelled"
		case errors.Is(w.request.Context().Err(), context.DeadlineExceeded):
			sample.Status = "error"
			sample.Reason = "timeout"
		case obs.ErrorCode != "":
			sample.Status = "error"
			sample.Reason = "upstream_stream_error"
		case sample.Stream && !obs.StreamDone:
			sample.Status = "error"
			sample.Reason = "stream_incomplete"
		case readErr != nil && !errors.Is(readErr, io.EOF) && !obs.StreamDone:
			sample.Status = "error"
			sample.Reason = "upstream_io_error"
		case obs.EventCount == 0 && obs.OutputChars == 0 && obs.ReasoningChars == 0 && (!obs.TokenCountKnown || obs.OutputTokens == 0):
			sample.Status = "error"
			sample.Reason = "empty_output"
		default:
			sample.Status = "success"
		}
		if sample.HTTPStatus == 0 {
			sample.Status = "error"
			sample.Reason = "upstream_network_error"
			if errors.Is(w.request.Context().Err(), context.DeadlineExceeded) {
				sample.Reason = "timeout"
			}
			if errors.Is(w.request.Context().Err(), context.Canceled) {
				sample.Status = "cancelled"
				sample.Reason = "request_cancelled"
			}
		}
		if obs.ReasoningTokens > 0 {
			sample.Reasoning = "on"
		} else if obs.ReasoningTokensKnown {
			sample.Reasoning = "off"
		}
		if sample.Reasoning != w.sample.Reasoning && w.sample.Reasoning != "unknown" && w.sample.Reasoning != "" {
			sample.Profile += ":observed_" + sample.Reasoning
		}
		sample.ObservedModel = truncateSpeedString(sample.ObservedModel, 200)
		sample.ErrorCode = truncateSpeedString(sample.ErrorCode, 100)
		if w.probe != nil {
			select {
			case w.probe.result <- sample:
			default:
			}
		}
	})
}

func truncateSpeedString(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}
