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
		respBody: &delayedKiroBodyReader{delay: 80 * time.Millisecond, body: body},
	}
	svc := NewKiroGatewayService(upstream, nil, nil)

	reqBody, _ := json.Marshal(map[string]any{
		"model":      "claude-opus-5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 32,
		"stream":     true,
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": 1000},
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(reqBody), Model: "claude-opus-5", Stream: true}

	// Same bind path as gateway_forward.go for account.IsKiro().
	k := startHeaderWaitKeepalive(c, 20*time.Millisecond, anthropicSSEPingFrame)
	bindPreContentStreamKeepalive(c, k)
	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), parsed, time.Now())
	k.stop()
	require.NoError(t, err)
	require.NotNil(t, result)

	out := rec.Body.String()
	require.GreaterOrEqual(t, strings.Count(out, "event: ping"), 1, "expected keepalive pings during Kiro stall, body=%q", out)
	require.Contains(t, out, "final answer")
	require.NotContains(t, out, "plan step one")
	require.NotNil(t, result.FirstTokenMs)
	// first_token_ms must reflect client-visible text, not an earlier thinking mark.
	require.GreaterOrEqual(t, *result.FirstTokenMs, 50, "first_token_ms=%d should cover the stall before visible text", *result.FirstTokenMs)
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
