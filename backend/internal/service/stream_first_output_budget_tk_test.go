//go:build unit

package service

import (
	"context"
	"errors"
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
	cfg.Gateway.StreamFirstOutputTimeoutSeconds = 40
	cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds = 90
	require.Equal(t, 40*time.Second, resolveStreamFirstOutputTimeout(cfg, false))
	require.Equal(t, 90*time.Second, resolveStreamFirstOutputTimeout(cfg, true))
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

func TestRemainingStreamFirstOutputBudget(t *testing.T) {
	require.Equal(t, 30*time.Second, remainingStreamFirstOutputBudget(nil, false, time.Time{}))
	started := time.Now().Add(-29 * time.Second)
	rem := remainingStreamFirstOutputBudget(nil, false, started)
	require.Greater(t, rem, time.Duration(0))
	require.LessOrEqual(t, rem, time.Second+50*time.Millisecond)
	require.Equal(t, time.Nanosecond, remainingStreamFirstOutputBudget(nil, false, time.Now().Add(-2*time.Minute)))
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
