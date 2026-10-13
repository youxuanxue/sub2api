//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

func TestResolveStreamFirstOutputTimeoutDefaults(t *testing.T) {
	require.Equal(t, 30*time.Second, resolveStreamFirstOutputTimeout(nil, false))
	require.Equal(t, 60*time.Second, resolveStreamFirstOutputTimeout(nil, true))

	cfg := &config.Config{}
	cfg.Gateway.StreamFirstOutputTimeoutSeconds = 20
	cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds = 45
	require.Equal(t, 20*time.Second, resolveStreamFirstOutputTimeout(cfg, false))
	require.Equal(t, 45*time.Second, resolveStreamFirstOutputTimeout(cfg, true))
}

func TestResolveGrokFirstByteHeaderTimeoutInheritsStreamSSOT(t *testing.T) {
	require.Equal(t, 30*time.Second, ResolveGrokFirstByteHeaderTimeout(nil))

	cfg := &config.Config{}
	cfg.Gateway.StreamFirstOutputTimeoutSeconds = 20
	require.Equal(t, 20*time.Second, ResolveGrokFirstByteHeaderTimeout(cfg))

	cfg.Gateway.GrokResponseHeaderTimeout = 25
	require.Equal(t, 25*time.Second, ResolveGrokFirstByteHeaderTimeout(cfg),
		"explicit grok override must win over stream SSOT")
}

func TestResolveStreamFirstOutputHighEffortIgnoresOpenAINamedKnob(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds = 0
	cfg.Gateway.OpenAIHighEffortFirstOutputTimeoutSeconds = 99
	require.Equal(t, 60*time.Second, resolveStreamFirstOutputTimeout(cfg, true),
		"universal high-effort must not inherit openai_high_effort_* when stream high-effort is zero")
}

func TestStreamFirstOutputGuardBudgetFired(t *testing.T) {
	parent := context.Background()
	ctx, guard := armStreamFirstOutputGuard(parent, 20*time.Millisecond)
	require.False(t, guard.BudgetFired(ctx, parent))
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("budget did not fire")
	}
	require.True(t, guard.BudgetFired(ctx, parent))
	require.False(t, guard.Committed())
}

func TestStreamFirstOutputGuardCommitDisarms(t *testing.T) {
	parent := context.Background()
	ctx, guard := armStreamFirstOutputGuard(parent, 30*time.Millisecond)
	guard.Commit()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, ctx.Err())
	require.True(t, guard.Committed())
	require.False(t, guard.BudgetFired(ctx, parent))
}

func TestAnthropicAndGeminiBodyHighEffort(t *testing.T) {
	require.False(t, anthropicBodyStreamFirstOutputHighEffort(nil))
	require.True(t, anthropicBodyStreamFirstOutputHighEffort([]byte(`{"thinking":{"type":"adaptive"}}`)))
	require.True(t, anthropicBodyStreamFirstOutputHighEffort([]byte(`{"output_config":{"effort":"high"}}`)))
	require.False(t, geminiBodyStreamFirstOutputHighEffort(nil))
	require.True(t, geminiBodyStreamFirstOutputHighEffort([]byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`)))
	require.True(t, geminiBodyStreamFirstOutputHighEffort([]byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":9000}}}`)))
}

func TestKiroStreamFirstOutputHighEffort(t *testing.T) {
	require.False(t, kiroStreamFirstOutputHighEffort(nil))
	require.False(t, kiroStreamFirstOutputHighEffort(&kiroproto.ClaudeRequest{}))
	require.True(t, kiroStreamFirstOutputHighEffort(&kiroproto.ClaudeRequest{
		Thinking: &kiroproto.ClaudeThinkingConfig{Type: "adaptive"},
	}))
	require.True(t, kiroStreamFirstOutputHighEffort(&kiroproto.ClaudeRequest{
		OutputConfig: &kiroproto.ClaudeOutputConfig{Effort: "high"},
	}))
}

func TestKiroFirstOutputBudgetWatchCloseUnblocksUpstreamRead(t *testing.T) {
	// Custom doer bodies are not closed by http.Request context cancel alone.
	// WatchClose must close the pipe so parseEventStream returns and the
	// attempt frees the concurrency slot promptly.
	pr, pw := io.Pipe()
	t.Cleanup(func() {
		_ = pw.Close()
		_ = pr.Close()
	})

	hang := &kiroHangBodyDoer{body: pr}
	account := &kiroproto.Account{
		AccessToken: "tok",
		ProfileArn:  "arn:aws:codewhisperer:us-east-1:1:profile/x",
	}

	parent := context.Background()
	ctx, guard := armStreamFirstOutputGuard(parent, 25*time.Millisecond)
	callback := &kiroproto.KiroStreamCallback{
		OnResponseBody: func(body io.ReadCloser) {
			guard.WatchClose(body)
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- kiroproto.CallKiroAPIWithDoerContext(ctx, hang, account, &kiroproto.KiroPayload{}, callback)
	}()

	select {
	case err := <-done:
		require.Error(t, err, "hung body must fail after WatchClose")
		require.True(t, guard.BudgetFired(ctx, parent), "budget must have fired")
		require.False(t, guard.Committed())
	case <-time.After(2 * time.Second):
		t.Fatal("Kiro EventStream read was not unblocked by WatchClose after first-output budget")
	}
}

type kiroHangBodyDoer struct {
	body io.ReadCloser
}

func (d *kiroHangBodyDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       d.body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestStreamFirstOutputFailoverIfBudgetFired_KeepaliveSetsSafeFlag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parent := context.Background()
	ctx, guard := armStreamFirstOutputGuard(parent, 15*time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("budget did not fire")
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.Equal(t, -1, c.Writer.Size())

	plain := streamFirstOutputFailoverIfBudgetFired(c, guard, ctx, parent)
	require.Error(t, plain)
	var fo *UpstreamFailoverError
	require.True(t, errors.As(plain, &fo))
	require.False(t, fo.SafeToFailoverAfterWrite)

	_, err := c.Writer.WriteString(anthropicSSEPingFrame)
	require.NoError(t, err)
	require.GreaterOrEqual(t, c.Writer.Size(), 0)

	after := streamFirstOutputFailoverIfBudgetFired(c, guard, ctx, parent)
	require.Error(t, after)
	require.True(t, errors.As(after, &fo))
	require.True(t, fo.SafeToFailoverAfterWrite)
}

// A running AfterFunc can have already read committed=false when Commit stops
// its timer. Stop does not join a running callback: cancel must be serialized
// with Commit so it cannot fire after Commit returns and useful output starts.
func TestStreamFirstOutputGuardCommitJoinsBudgetCancellation(t *testing.T) {
	ctx, guard := armStreamFirstOutputGuard(context.Background(), time.Hour)
	originalCancel := guard.cancel
	entered := make(chan struct{})
	release := make(chan struct{})
	// Arm a long timer first, install the cancellation barrier before resetting
	// it, then drive the real AfterFunc callback (not a copy of its branches).
	guard.cancel = func() { close(entered); <-release; originalCancel() }
	guard.timer.Reset(time.Nanosecond)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("budget callback did not start")
	}
	done := make(chan struct{})
	go func() { guard.Commit(); close(done) }()
	select {
	case <-done:
		close(release)
		<-ctx.Done()
		t.Fatal("Commit returned while the budget callback could still cancel newly committed output")
	case <-time.After(25 * time.Millisecond):
		close(release)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Commit did not finish after budget cancellation")
	}
	require.ErrorIs(t, ctx.Err(), context.Canceled, "the already-running budget cancellation must complete before Commit returns")
	require.True(t, guard.Committed(), "late output still prevents replay")
}

func TestStreamFirstOutputGuardLateCommitStillClosesTimedOutBody(t *testing.T) {
	ctx, guard := armStreamFirstOutputGuard(context.Background(), time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("budget did not fire")
	}
	guard.Commit() // late output forbids replay but cannot revive the canceled read
	closed := make(chan struct{})
	guard.WatchClose(streamBudgetCloseRecorder{closed})
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late Commit prevented timed-out body cleanup")
	}
	require.True(t, guard.Committed())
	require.False(t, guard.BudgetFired(ctx, context.Background()), "visible late output still forbids failover")
}

type streamBudgetCloseRecorder struct{ closed chan struct{} }

func (c streamBudgetCloseRecorder) Close() error { close(c.closed); return nil }
