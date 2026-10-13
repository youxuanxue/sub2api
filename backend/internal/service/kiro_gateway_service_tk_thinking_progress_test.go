//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// kiroPacedBody emits EventStream frames on a delay schedule. io.Pipe has no
// buffer, so a write before the gateway is reading blocks until the read.
type kiroPacedUpstream struct {
	body  io.ReadCloser
	calls atomic.Int32
}

func (u *kiroPacedUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, io.EOF
}

func (u *kiroPacedUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       u.body,
	}, nil
}

func kiroThinkingProgressConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval:                   0,
		StreamFirstOutputTimeoutSeconds:           1,
		StreamFirstOutputHighEffortTimeoutSeconds: 1,
	}}
}

func TestKiroStreaming_ReasoningFramesPastSilenceBudgetStillComplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	go func() {
		defer writer.Close()
		frame := buildKiroEventStreamMessage("reasoningContentEvent", []byte(`{"text":"planning the review"}`))
		for i := 0; i < 4; i++ {
			time.Sleep(400 * time.Millisecond)
			if _, err := writer.Write(frame); err != nil {
				return
			}
		}
		time.Sleep(400 * time.Millisecond)
		done := appendKiroTerminalStop(
			buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"review text"}`)),
			"END_TURN",
		)
		_, _ = writer.Write(done)
	}()

	svc := NewKiroGatewayService(&kiroPacedUpstream{body: reader}, nil, nil)
	svc.SetGatewayConfig(kiroThinkingProgressConfig())
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"review the draft"}],"max_tokens":256,"stream":true,"thinking":{"type":"adaptive"}}`)
	start := time.Now()
	result, err := svc.Forward(
		c.Request.Context(), c, newKiroAccountForTest(),
		&ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true},
		start,
	)
	require.NoError(t, err)
	require.NotNil(t, result, "completed stream must return usage so the handler can bill")
	require.Greater(t, time.Since(start), time.Second, "visible text must arrive after the 1s silence budget")
	require.Contains(t, rec.Body.String(), "review text")
	require.NotContains(t, rec.Body.String(), "planning the review")
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
}

func TestKiroStreaming_ReasoningThenSilenceStillFailoversWithoutCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	upstream := &kiroPacedUpstream{body: reader}
	go func() {
		time.Sleep(800 * time.Millisecond)
		frame := buildKiroEventStreamMessage("reasoningContentEvent", []byte(`{"text":"still thinking"}`))
		_, _ = writer.Write(frame)
		time.Sleep(5 * time.Second)
		_ = writer.Close()
	}()

	svc := NewKiroGatewayService(upstream, nil, nil)
	svc.SetGatewayConfig(kiroThinkingProgressConfig())
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"review the draft"}],"max_tokens":256,"stream":true,"thinking":{"type":"enabled","budget_tokens":16000}}`)
	start := time.Now()
	result, err := svc.Forward(
		c.Request.Context(), c, newKiroAccountForTest(),
		&ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true},
		start,
	)
	elapsed := time.Since(start)
	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, GatewayFailureReason("first_output_unavailable"), failover.Reason)
	require.Greater(t, elapsed, 1500*time.Millisecond, "one reasoning frame must extend past the original 1s budget, elapsed=%s", elapsed)
	require.Less(t, elapsed, 4*time.Second, "silence after that frame must still failover, elapsed=%s", elapsed)
	require.EqualValues(t, 1, upstream.calls.Load(), "canceled attempt must not open another Kiro endpoint")
	require.NotContains(t, rec.Body.String(), "still thinking")
	require.NotContains(t, rec.Body.String(), "event: message_stop")
	require.False(t, strings.Contains(rec.Body.String(), "event: error"))
}
