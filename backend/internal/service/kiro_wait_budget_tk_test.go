//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// kiroPacedBody emits EventStream frames on a delay schedule. io.Pipe has no
// buffer, so a write before the gateway is reading blocks until the read.
type kiroPacedUpstream struct {
	body   io.ReadCloser
	status int
	calls  atomic.Int32
}

func (u *kiroPacedUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, io.EOF
}

func (u *kiroPacedUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls.Add(1)
	status := u.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       u.body,
	}, nil
}

func kiroFixedWaitConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{
		StreamKeepaliveInterval:                 0,
		KiroFirstOutputTimeoutSeconds:           1,
		KiroHighEffortFirstOutputTimeoutSeconds: 3,
	}}
}

func TestKiroStreaming_ReasoningCompletesWithinHighBudget(t *testing.T) {
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
	svc.SetGatewayConfig(kiroFixedWaitConfig())
	body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"review the draft"}],"max_tokens":256,"stream":true,"thinking":{"type":"adaptive"}}`)
	start := time.Now()
	result, err := svc.Forward(
		c.Request.Context(), c, newKiroAccountForTest(),
		&ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true},
		start,
	)
	require.NoError(t, err)
	require.NotNil(t, result, "completed stream must return usage so the handler can bill")
	require.Greater(t, time.Since(start), time.Second, "visible text must arrive after the ordinary budget, within the fixed high budget")
	require.Contains(t, rec.Body.String(), "review text")
	require.Contains(t, rec.Body.String(), `"thinking":"planning the review"`)
	require.Less(t, strings.Index(rec.Body.String(), `"type":"thinking_delta"`), strings.Index(rec.Body.String(), "review text"))
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
}

func TestKiroWaitBudgetPolicy(t *testing.T) {
	for _, tc := range []struct {
		body string
		high bool
	}{
		{`{"model":"claude-sonnet-4-5"}`, false},
		{`{"model":"claude-opus-5-5"}`, true},
		{`{"model":"claude-fable-5-1"}`, true},
		{`{"model":"claude-opus-4.6"}`, true},
		{`{"model":"claude-sonnet-4-5","thinking":{"type":"adaptive"}}`, true},
		{`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled"}}`, true},
		{`{"model":"claude-sonnet-4-5","output_config":{"effort":"high"}}`, true},
		{`{"model":"claude-sonnet-4-5","output_config":{"effort":"xhigh"}}`, true},
		{`{"model":"claude-sonnet-4-5","output_config":{"effort":"max"}}`, true},
		{`{"model":"not-claude-opus-fake","thinking":{"type":"disabled","budget_tokens":16000}}`, false},
		{`{"model":"claude-fablet-5"}`, false},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var req kiroproto.ClaudeRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &req))
			require.Equal(t, tc.high, kiroStreamFirstOutputHighEffort(&req))
			want := defaultKiroFirstOutputTimeout
			if tc.high {
				want = defaultKiroHighEffortFirstOutputTimeout
			}
			cfg := &config.Config{Gateway: config.GatewayConfig{StreamFirstOutputTimeoutSeconds: 1, OpenAIFirstOutputTimeoutSeconds: 2}}
			require.Equal(t, want, resolveKiroWaitTimeout(cfg, tc.high), "Kiro cannot inherit another platform's knobs")
		})
	}
}

func kiroBudgetContext(t *testing.T, budget time.Duration) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Set(kiroWaitBudgetKey, kiroWaitBudget{deadline: time.Now().Add(budget), budget: budget})
	return c, rec
}

func TestKiroWaitBudgetProgressDoesNotExtendDeadline(t *testing.T) {
	// Non-streaming holds every frame until the turn completes, so neither
	// reasoning nor partial text commits the wait or reaches the client.
	// Streaming reasoning is covered by TestKiroStreamingThinkingCommitsWaitBudget.
	for _, content := range []string{"reasoningContentEvent", "assistantResponseEvent"} {
		t.Run(content, func(t *testing.T) {
			c, rec := kiroBudgetContext(t, 200*time.Millisecond)
			reader, writer := io.Pipe()
			t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer writer.Close()
				frame := buildKiroEventStreamMessage(content, []byte(`{"text":"thinking","content":"partial answer"}`))
				for {
					if _, err := writer.Write(frame); err != nil {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}()
			upstream := &kiroPacedUpstream{body: reader}
			svc := NewKiroGatewayService(upstream, nil, nil)
			body := []byte(`{"model":"claude-opus-5-5","stream":false,"max_tokens":256,"messages":[{"role":"user","content":"review"}]}`)
			start := time.Now()
			result, err := svc.Forward(c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: false}, start)
			require.Nil(t, result)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Equal(t, GatewayFailureReason("kiro_wait_budget_exhausted"), failover.Reason)
			require.False(t, failover.ShouldRetryNextAccount())
			require.Less(t, time.Since(start), time.Second)
			require.EqualValues(t, 1, upstream.calls.Load(), "canceled attempt must not open another Kiro endpoint")
			require.Empty(t, rec.Body.String(), "internal partial output must not escape")
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("body reader was not released")
			}
		})
	}
}

func TestKiroStreamingThinkingCommitsWaitBudget(t *testing.T) {
	c, rec := kiroBudgetContext(t, 200*time.Millisecond)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	go func() {
		defer writer.Close()
		if _, err := writer.Write(buildKiroEventStreamMessage("reasoningContentEvent", []byte(`{"text":"planning"}`))); err != nil {
			return
		}
		time.Sleep(400 * time.Millisecond)
		_, _ = writer.Write(appendKiroTerminalStop(
			buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"answer"}`)),
			"END_TURN",
		))
	}()
	svc := NewKiroGatewayService(&kiroPacedUpstream{body: reader}, nil, nil)
	body := []byte(`{"model":"claude-opus-5-5","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"review"}]}`)
	start := time.Now()
	result, err := svc.Forward(c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5-5", Stream: true}, start)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Greater(t, time.Since(start), 200*time.Millisecond, "thinking must commit the wait so the answer can arrive after the original deadline")
	out := rec.Body.String()
	require.Contains(t, out, `"thinking":"planning"`)
	require.Contains(t, out, "answer")
	require.Less(t, strings.Index(out, `"type":"thinking_delta"`), strings.Index(out, "answer"))
}

func TestKiroWaitBudgetRetriesShareDeadline(t *testing.T) {
	c, _ := kiroBudgetContext(t, 200*time.Millisecond)
	req := &kiroproto.ClaudeRequest{Model: "claude-sonnet-4-5"}
	ctx, guard, release, err := armKiroWaitBudget(context.Background(), c, nil, req, time.Now())
	require.NoError(t, err)
	first := getKiroWaitBudget(c, nil, req, time.Now())
	time.Sleep(80 * time.Millisecond)
	release()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.False(t, guard.budgetCanceled.Load())
	req.Model = "claude-opus-5-5"
	ctx, guard, release, err = armKiroWaitBudget(context.Background(), c, nil, req, time.Now())
	require.NoError(t, err)
	defer release()
	require.Equal(t, first, getKiroWaitBudget(c, nil, req, time.Now()))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("retry reset the budget")
	}
	var terminal *UpstreamFailoverError
	require.ErrorAs(t, kiroWaitBudgetError(c, guard, ctx, context.Background()), &terminal)
	require.False(t, terminal.ShouldRetryNextAccount())
	_, _, _, err = armKiroWaitBudget(context.Background(), c, nil, req, time.Now())
	require.ErrorAs(t, err, &terminal, "no new attempt after exhaustion")
}

func TestKiroNonStreamingCallerCancellation(t *testing.T) {
	c, rec := kiroBudgetContext(t, time.Second)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	go func() {
		_, _ = writer.Write(buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"partial"}`)))
		cancel()
	}()
	svc := NewKiroGatewayService(&kiroPacedUpstream{body: reader}, nil, nil)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":"review"}]}`)
	result, err := svc.Forward(ctx, c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5"}, time.Now())
	require.Nil(t, result)
	require.ErrorIs(t, err, context.Canceled)
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)
	require.Empty(t, rec.Body.String())
}

func TestKiroStreamingVisibleOutputStopsWaitBudget(t *testing.T) {
	c, rec := kiroBudgetContext(t, 100*time.Millisecond)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	go func() {
		defer writer.Close()
		if _, err := writer.Write(buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"answer"}`))); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
		_, _ = writer.Write(buildKiroEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`)))
	}()
	svc := NewKiroGatewayService(&kiroPacedUpstream{body: reader}, nil, nil)
	body := []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":256,"messages":[{"role":"user","content":"review"}]}`)
	result, err := svc.Forward(c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5", Stream: true}, time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "answer")
	require.Contains(t, rec.Body.String(), "event: message_stop")
}

func TestKiroWaitBudgetClosesStalledErrorBody(t *testing.T) {
	c, rec := kiroBudgetContext(t, 100*time.Millisecond)
	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	upstream := &kiroPacedUpstream{body: reader, status: http.StatusBadRequest}
	svc := NewKiroGatewayService(upstream, nil, nil)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":256,"messages":[{"role":"user","content":"review"}]}`)
	result, err := svc.Forward(c.Request.Context(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-5"}, time.Now())
	require.Nil(t, result)
	var terminal *UpstreamFailoverError
	require.ErrorAs(t, err, &terminal)
	require.False(t, terminal.ShouldRetryNextAccount())
	require.EqualValues(t, 1, upstream.calls.Load())
	require.Empty(t, rec.Body.String())
}
