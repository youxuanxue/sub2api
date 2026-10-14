//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// delayedKiroBodyReader emits nothing for delay, then the full EventStream body.
// Models Kiro spending a long time before any client-visible bytes arrive.
type delayedKiroBodyReader struct {
	delay time.Duration
	body  []byte
	sent  bool
}

func (r *delayedKiroBodyReader) Read(p []byte) (int, error) {
	if !r.sent {
		time.Sleep(r.delay)
		r.sent = true
	}
	if len(r.body) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.body)
	r.body = r.body[n:]
	if len(r.body) == 0 {
		return n, io.EOF
	}
	return n, nil
}

func (r *delayedKiroBodyReader) Close() error { return nil }

type kiroStallUpstream struct {
	respBody io.ReadCloser
}

func (u *kiroStallUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, io.EOF
}

func (u *kiroStallUpstream) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       u.respBody,
	}, nil
}

func TestKiroStreaming_HeaderWaitKeepaliveDuringThinkingStall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"plan step one"}`))
	textFrame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"final answer"}`))
	body := append(reasoningFrame, textFrame...)
	body = appendKiroTerminalStop(body, "END_TURN")

	upstream := &kiroStallUpstream{
		respBody: &delayedKiroBodyReader{delay: 1100 * time.Millisecond, body: body},
	}
	svc := NewKiroGatewayService(upstream, nil, nil)
	// Exercise production SSOT: Forward → ensureKiroStreamingKeepalive (not a
	// hand-bound emitter). Interval=1s is valid for SetGatewayConfig; delay
	// must exceed one tick so message_start+ping appear before visible text.
	svc.SetGatewayConfig(&config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}})

	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1000},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-opus-5", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Nil(t, boundPreContentStreamKeepalive(c), "defer must stop keepalive before Forward returns")

	out := rec.Body.String()
	require.GreaterOrEqual(t, strings.Count(out, "event: ping"), 1, "expected keepalive pings during Kiro stall, body=%q", out)
	require.Contains(t, out, "event: message_start", "Claude Code requires message_start before ping on Kiro stalls")
	require.Less(t, strings.Index(out, "event: message_start"), strings.Index(out, "event: ping"),
		"message_start must precede the first ping so Claude Code keeps the late-first-token stream")
	require.Contains(t, out, "final answer")
	require.NotContains(t, out, "plan step one")
	require.Equal(t, 1, strings.Count(out, "event: message_start"), "keepalive + encoder must not duplicate message_start")
	require.NotNil(t, result.FirstTokenMs)
	// first_token_ms must reflect client-visible text, not an earlier thinking mark.
	require.GreaterOrEqual(t, *result.FirstTokenMs, 1000, "first_token_ms=%d should cover the stall before visible text", *result.FirstTokenMs)
}

func TestKiroStreaming_FastFailoverPathWritesNoKeepalive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	k := startHeaderWaitKeepalive(c, 500*time.Millisecond, anthropicSSEPingFrame)
	bindPreContentStreamKeepalive(c, k)

	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-opus-5", Stream: true}

	_, err := NewKiroGatewayService(&kiroFakeUpstream{}, nil, nil).Forward(
		context.Background(), c, newKiroAccountForTest(), parsed, time.Now(),
	)
	k.stop()
	require.Error(t, err)
	require.Empty(t, rec.Body.String(), "fast empty upstream must not emit keepalive pings before failover")
	require.False(t, c.Writer.Written(), "failover gate must remain clear")
}

func TestKiroStreaming_FirstTokenIgnoresThinkingOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	reasoningFrame := buildKiroEventStreamMessage("reasoningContentEvent",
		[]byte(`{"text":"hidden plan"}`))
	textFrame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"visible"}`))
	body := append(reasoningFrame, textFrame...)
	body = appendKiroTerminalStop(body, "END_TURN")

	svc := NewKiroGatewayService(&kiroFakeUpstream{body: body}, nil, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1000},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-opus-5", Stream: true}

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.FirstTokenMs)
	require.Contains(t, rec.Body.String(), "visible")
	require.NotContains(t, rec.Body.String(), "hidden plan")
}

func TestKiroStreaming_FixedBudgetAfterKeepaliveStopsRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	svc := NewKiroGatewayService(&kiroStallUpstream{respBody: reader}, nil, nil)
	svc.SetGatewayConfig(&config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1, KiroHighEffortFirstOutputTimeoutSeconds: 2}})
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":true}`)
	_, err := svc.Forward(c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Stream: true}, time.Now())
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, GatewayFailureReason("kiro_wait_budget_exhausted"), failover.Reason)
	require.True(t, failover.SafeToFailoverAfterWrite)
	require.Contains(t, rec.Body.String(), "event: message_start")
	require.Contains(t, rec.Body.String(), "event: ping")
	require.NotContains(t, rec.Body.String(), "event: error", "the handler owns the single terminal error")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	require.Nil(t, boundPreContentStreamKeepalive(c))
	events, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	require.Len(t, events.([]*OpsUpstreamErrorEvent), 1)
	require.Equal(t, "first_output_timeout", events.([]*OpsUpstreamErrorEvent)[0].Kind)

	// A replacement account may not restart an exhausted request budget.
	result, err := NewKiroGatewayService(&kiroFakeUpstream{}, nil, nil).Forward(
		c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Stream: true}, time.Now())
	require.Nil(t, result)
	require.ErrorAs(t, err, &failover)
	require.False(t, failover.ShouldRetryNextAccount())
	require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start"))
	require.NotContains(t, rec.Body.String(), "event: message_stop")
}

type kiroCallerCancelUpstream struct{ cancel context.CancelFunc }

func (u *kiroCallerCancelUpstream) Do(req *http.Request, proxy string, accountID int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxy, accountID, concurrency, nil)
}

func (u *kiroCallerCancelUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	time.Sleep(1100 * time.Millisecond)
	u.cancel()
	<-req.Context().Done()
	return nil, req.Context().Err()
}

func TestKiroStreaming_CallerCancelAfterKeepaliveDoesNotRecordProviderFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":true}`)
	svc := NewKiroGatewayService(&kiroCallerCancelUpstream{cancel: cancel}, nil, nil)
	svc.SetGatewayConfig(&config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}})
	_, err := svc.Forward(
		ctx, c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Stream: true}, time.Now())
	require.ErrorIs(t, err, context.Canceled)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
	require.Contains(t, rec.Body.String(), "event: ping")
	require.NotContains(t, rec.Body.String(), "event: error")
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded, "caller cancellation must not become a provider failure")
}
