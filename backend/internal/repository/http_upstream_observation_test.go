package repository

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestHTTPUpstreamObservesResetAfterHeadersWithoutClosingHealthyStreams(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	ctx := logger.IntoContext(t.Context(), zap.New(core).With(zap.String("request_id", "diagnostic-test")))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/reset" {
			_, _ = io.WriteString(w, "data: partial\n\n")
			_ = http.NewResponseController(w).Flush()
			panic(http.ErrAbortHandler) // Real HTTP/2 RST_STREAM INTERNAL_ERROR.
		}
		_, _ = io.WriteString(w, "data: complete\n\n")
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	s, do := lifecycleClient(t, srv, false)
	// Warm the connection so the failure carries reuse evidence.
	for _, path := range []string{"/ok", "/reset", "/ok"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path+"?token=secret", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer secret")
		resp, err := do(req)
		require.NoError(t, err)
		require.Equal(t, 2, resp.ProtoMajor)
		body, err := io.ReadAll(resp.Body)
		if path == "/reset" {
			require.ErrorContains(t, err, "INTERNAL_ERROR")
			require.Equal(t, "data: partial\n\n", string(body))
		} else {
			require.NoError(t, err)
			require.Equal(t, "data: complete\n\n", string(body))
		}
		require.NoError(t, resp.Body.Close())
	}
	requireNoUpstreamInFlight(t, s)
	events := logs.FilterMessage("upstream_transport_failure").All()
	require.Len(t, events, 1)
	fields := events[0].ContextMap()
	require.Equal(t, "diagnostic-test", fields["request_id"])
	require.Equal(t, "body_read", fields["failure_stage"])
	require.Equal(t, "http2_stream_reset", fields["failure_kind"])
	require.Equal(t, "INTERNAL_ERROR", fields["http2_error_code"])
	require.Equal(t, int64(len("data: partial\n\n")), fields["body_bytes_read"])
	require.Equal(t, true, fields["connection_reused"])
	require.Equal(t, "h2", fields["alpn"])
	require.Equal(t, int64(200), fields["upstream_status"])
	for _, value := range fields {
		if text, ok := value.(string); ok {
			require.NotContains(t, text, "secret")
			require.NotContains(t, text, "partial")
		}
	}
}

type bodyReadError struct{ err error }

func (b bodyReadError) Read([]byte) (int, error) { return 0, b.err }
func (b bodyReadError) Close() error             { return nil }

func TestHTTPUpstreamBodyOutcomeOwnsProxyFallbackHealth(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{OpenAIHTTP2: config.GatewayOpenAIHTTP2Config{
		Enabled: true, AllowProxyFallbackToHTTP1: true, FallbackErrorThreshold: 2,
	}}}
	s, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	proxy := "http://proxy.test:8080"
	entry, err := s.acquireClientWithProfile(proxy, 1, 1, service.HTTPUpstreamProfileOpenAI)
	require.NoError(t, err)
	atomic.AddInt64(&entry.inFlight, -1)
	reset := errors.New("stream error: stream ID 3; INTERNAL_ERROR; received from peer")
	s.recordOpenAIHTTP2Failure(service.HTTPUpstreamProfileOpenAI, entry.protocolMode, proxy, reset)
	entry.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: bodyReadError{reset}}, nil
	})}
	ctx := service.WithHTTPUpstreamProfile(t.Context(), service.HTTPUpstreamProfileOpenAI)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://upstream.test", nil)
	require.NoError(t, err)
	resp, err := s.Do(req, proxy, 1, 1)
	require.NoError(t, err)
	require.False(t, s.isOpenAIHTTP2FallbackActive(proxy))
	_, err = io.ReadAll(resp.Body)
	require.ErrorIs(t, err, reset)
	require.True(t, s.isOpenAIHTTP2FallbackActive(proxy), "200 headers must not erase previous failures")
	require.NoError(t, resp.Body.Close())
	requireNoUpstreamInFlight(t, s)

	// A fully read response, rather than headers or early Close, resets errors.
	state := &openAIHTTP2FallbackState{errorCount: 1}
	s.openAIHTTP2Fallbacks.Store(proxy, state)
	entry.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("done"))}, nil
	})}
	resp, err = s.Do(req, proxy, 1, 1)
	require.NoError(t, err)
	require.Equal(t, 1, state.errorCount)
	_, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Zero(t, state.errorCount)
	require.NoError(t, resp.Body.Close())
}

func TestObservedUpstreamBodyEarlyCloseAndEOF(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		closeFirst bool
		want       int
	}{
		{"early close", context.Canceled, true, 0},
		{"EOF", io.EOF, false, 1},
		{"reset", errors.New("http2: client connection lost"), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			b := &observedUpstreamBody{ReadCloser: bodyReadError{tc.err}, onResult: func(err error, count int64) {
				calls++
				require.ErrorIs(t, err, tc.err)
				require.Zero(t, count)
			}}
			if tc.closeFirst {
				require.NoError(t, b.Close())
			}
			for i := 0; i < 2; i++ {
				_, _ = b.Read(make([]byte, 1))
			}
			require.Equal(t, tc.want, calls)
		})
	}
	kind, _, _ := upstreamTransportFailureKind(errors.New("http2: client connection lost"))
	require.Equal(t, "http2_connection_lost", kind)
	kind, _, _ = upstreamTransportFailureKind(context.Canceled)
	require.Equal(t, "canceled", kind)
}
