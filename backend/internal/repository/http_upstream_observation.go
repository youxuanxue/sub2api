package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// Observe the whole attempt, including failures after 200 headers. Do not log
// request URLs, headers, bodies or raw errors: all may contain credentials.
type upstreamObservation struct {
	ctx       context.Context
	started   time.Time
	accountID int64
	mode      string
	host      string
	proxied   bool
	mu        sync.Mutex
	reused    bool
	idle      time.Duration
	peer      string
}

func observeUpstreamRequest(req *http.Request, entry *upstreamClientEntry, accountID int64) (*http.Request, *upstreamObservation) {
	o := &upstreamObservation{ctx: req.Context(), started: time.Now(), accountID: accountID,
		mode: entry.protocolMode, host: req.URL.Hostname(), proxied: entry.proxyKey != directProxyKey}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.reused, o.idle = info.Reused, info.IdleTime
		if info.Conn != nil && info.Conn.RemoteAddr() != nil {
			o.peer = info.Conn.RemoteAddr().String()
		}
	}}
	return req.WithContext(httptrace.WithClientTrace(req.Context(), trace)), o
}

// Go's bundled HTTP/2 and x/net/http2 do not always expose the same concrete
// error type. Parse only the stable, bounded code/id portion of a stream error.
var upstreamHTTP2StreamError = regexp.MustCompile(`stream error: stream ID ([0-9]+); ([A-Z_]+)`)

func upstreamTransportFailureKind(err error) (kind, streamID, code string) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, http.ErrBodyReadAfterClose):
		return "canceled", "", ""
	case isUpstreamTimeoutError(err):
		return "timeout", "", ""
	}
	msg := err.Error()
	if match := upstreamHTTP2StreamError.FindStringSubmatch(msg); len(match) == 3 {
		return "http2_stream_reset", match[1], match[2]
	}
	if strings.Contains(msg, "http2: client connection lost") {
		return "http2_connection_lost", "", ""
	}
	if strings.Contains(strings.ToLower(msg), "goaway") {
		return "http2_goaway", "", ""
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return "unexpected_eof", "", ""
	}
	return "transport_error", "", ""
}

func (o *upstreamObservation) failure(stage string, resp *http.Response, bytesRead int64, err error) {
	kind, streamID, code := upstreamTransportFailureKind(err)
	if kind == "canceled" {
		return
	}
	fields := []zap.Field{
		zap.Int64("account_id", o.accountID), zap.String("upstream_host", o.host),
		zap.String("protocol_mode", o.mode), zap.Bool("proxied", o.proxied),
		zap.String("failure_stage", stage), zap.String("failure_kind", kind),
		zap.Int64("body_bytes_read", bytesRead), zap.Int64("elapsed_ms", time.Since(o.started).Milliseconds()),
		zap.String("http2_stream_id", streamID), zap.String("http2_error_code", code),
	}
	o.mu.Lock()
	fields = append(fields, zap.Bool("connection_reused", o.reused),
		zap.Int64("connection_idle_ms", o.idle.Milliseconds()), zap.String("peer_address", o.peer))
	o.mu.Unlock()
	if resp != nil {
		fields = append(fields, zap.Int("upstream_status", resp.StatusCode), zap.String("upstream_protocol", resp.Proto))
		if resp.TLS != nil {
			fields = append(fields, zap.String("alpn", resp.TLS.NegotiatedProtocol))
		}
	}
	logger.FromContext(o.ctx).Warn("upstream_transport_failure", fields...)
}

type observedUpstreamBody struct {
	io.ReadCloser
	onResult func(error, int64)
	bytes    atomic.Int64
	closed   atomic.Bool
	once     sync.Once
}

func (b *observedUpstreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	count := b.bytes.Add(int64(n))
	if err != nil && !b.closed.Load() {
		b.once.Do(func() { b.onResult(err, count) })
	}
	return n, err
}

func (b *observedUpstreamBody) Close() error {
	// Closing a live stream cancels its read; that is not a transport failure.
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func (s *httpUpstreamService) observeResponseBody(resp *http.Response, o *upstreamObservation, entry *upstreamClientEntry, profile service.HTTPUpstreamProfile) {
	if resp.Body == nil {
		return
	}
	resp.Body = &observedUpstreamBody{ReadCloser: resp.Body, onResult: func(err error, count int64) {
		if errors.Is(err, io.EOF) {
			s.recordOpenAIHTTP2Success(profile, entry.protocolMode, entry.proxyKey)
			return
		}
		s.recordOpenAIHTTP2Failure(profile, entry.protocolMode, entry.proxyKey, err)
		o.failure("body_read", resp, count, err)
	}}
}
