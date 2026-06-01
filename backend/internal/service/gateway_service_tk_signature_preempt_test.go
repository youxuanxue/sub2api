package service

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// fakeSigPreemptCache lets unit tests dictate Arm/IsArmed behavior without
// touching Redis.
type fakeSigPreemptCache struct {
	armed     atomic.Bool
	armErr    error
	isArmErr  error
	armReturn struct {
		count    int64
		armedNow bool
	}
	armCalls    atomic.Int32
	isArmCalls  atomic.Int32
	lastAccount atomic.Int64
}

func (f *fakeSigPreemptCache) ArmIfThreshold(_ context.Context, accountID int64, _, _, _ int) (int64, bool, error) {
	f.armCalls.Add(1)
	f.lastAccount.Store(accountID)
	if f.armErr != nil {
		return 0, false, f.armErr
	}
	if f.armReturn.armedNow {
		f.armed.Store(true)
	}
	return f.armReturn.count, f.armReturn.armedNow, nil
}

func (f *fakeSigPreemptCache) IsArmed(_ context.Context, accountID int64) (bool, error) {
	f.isArmCalls.Add(1)
	f.lastAccount.Store(accountID)
	if f.isArmErr != nil {
		return false, f.isArmErr
	}
	return f.armed.Load(), nil
}

func newTestGin() *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	return c
}

func newTestAccount() *Account {
	return &Account{
		ID:       42,
		Name:     "test-account",
		Platform: PlatformAnthropic,
	}
}

func getOpsEvents(c *gin.Context) []*OpsUpstreamErrorEvent {
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	arr, _ := v.([]*OpsUpstreamErrorEvent)
	return arr
}

func hasOpsEventKind(c *gin.Context, kind string) bool {
	for _, ev := range getOpsEvents(c) {
		if ev != nil && ev.Kind == kind {
			return true
		}
	}
	return false
}

// --- armSigPreemptOnError ---

func TestArmSigPreempt_NilCache_NoOp(t *testing.T) {
	s := &GatewayService{}
	c := newTestGin()
	s.armSigPreemptOnError(context.Background(), c, newTestAccount())
	require.Empty(t, getOpsEvents(c), "no ops event when cache is nil")
}

func TestArmSigPreempt_NilAccount_NoOp(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()
	s.armSigPreemptOnError(context.Background(), c, nil)
	require.Equal(t, int32(0), cache.armCalls.Load(), "nil account skips cache call")
}

func TestArmSigPreempt_BelowThreshold_NoEvent(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	cache.armReturn.count = 2
	cache.armReturn.armedNow = false
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	s.armSigPreemptOnError(context.Background(), c, newTestAccount())

	require.Equal(t, int32(1), cache.armCalls.Load())
	require.False(t, hasOpsEventKind(c, "signature_preempt_armed"), "armed event only when armedNow=true")
}

func TestArmSigPreempt_AtThreshold_EmitsEvent(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	cache.armReturn.count = 3
	cache.armReturn.armedNow = true
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	s.armSigPreemptOnError(context.Background(), c, newTestAccount())

	require.True(t, hasOpsEventKind(c, "signature_preempt_armed"), "must emit ops event on arm transition")
	events := getOpsEvents(c)
	require.Len(t, events, 1)
	require.Equal(t, "signature_preempt_armed", events[0].Kind)
	require.Equal(t, int64(42), events[0].AccountID)
	require.Equal(t, "signature_error_threshold_crossed", events[0].Message)
	require.Empty(t, events[0].Detail, "Detail intentionally unused — count is implicit at arm transition")
}

func TestArmSigPreempt_RedisError_FailOpen(t *testing.T) {
	cache := &fakeSigPreemptCache{armErr: errors.New("redis down")}
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	// Must not panic and must not emit a misleading ops event.
	s.armSigPreemptOnError(context.Background(), c, newTestAccount())
	require.False(t, hasOpsEventKind(c, "signature_preempt_armed"))
}

// --- applySigPreemptIfArmed ---

func TestApplyPreempt_NilCache_PassesBodyThrough(t *testing.T) {
	s := &GatewayService{}
	c := newTestGin()
	body := []byte(`{"foo":"bar"}`)
	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)
	require.Equal(t, body, out)
	require.Empty(t, getOpsEvents(c))
}

func TestApplyPreempt_NotArmed_PassesBodyThrough(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)
	require.Equal(t, body, out)
	require.Empty(t, getOpsEvents(c))
}

func TestApplyPreempt_Armed_StripsThinkingBlocks(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	cache.armed.Store(true)
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	// Body with a thinking block at the top level and on an assistant message.
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"hidden reasoning","signature":"sig-x"}]},{"role":"user","content":"continue"}]}`)

	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)

	require.NotEqual(t, body, out, "armed run with thinking content must transform body")
	require.False(t, bytes.Contains(out, []byte(`"type":"thinking"`)),
		"thinking block must be removed from output, got: %s", string(out))
	require.True(t, hasOpsEventKind(c, "signature_preempt_applied"), "must emit ops event on apply")

	events := getOpsEvents(c)
	require.Len(t, events, 1)
	require.Equal(t, "thinking_blocks_stripped", events[0].Message)
}

func TestApplyPreempt_Armed_NoThinkingContent_StaysSilent(t *testing.T) {
	// Armed-but-nothing-to-strip is silent: the cooldown check still ran, but
	// emitting an ops event per request would flood ops_error_logs on accounts
	// with steady non-thinking traffic during cooldown. Behavior of armed
	// path is still asserted via the IsArmed call count.
	cache := &fakeSigPreemptCache{}
	cache.armed.Store(true)
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)

	require.Equal(t, body, out, "no-thinking body returned unchanged even when armed")
	require.Equal(t, int32(1), cache.isArmCalls.Load(), "armed flag must still be checked")
	require.Empty(t, getOpsEvents(c), "no-op preempt must not emit ops event")
}

func TestApplyPreempt_IsArmedError_FailOpen(t *testing.T) {
	cache := &fakeSigPreemptCache{isArmErr: errors.New("redis disconnected")}
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()
	body := []byte(`{"foo":"bar"}`)
	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)
	require.Equal(t, body, out, "fail-open returns original body when cache errors")
	require.Empty(t, getOpsEvents(c), "no events emitted on cache error")
}

// --- HasAnthropicSigPreemptCache wire-assertion smoke ---

func TestHasAnthropicSigPreemptCache_Setter(t *testing.T) {
	s := &GatewayService{}
	require.False(t, s.HasAnthropicSigPreemptCache())
	s.SetAnthropicSigPreemptCache(&fakeSigPreemptCache{})
	require.True(t, s.HasAnthropicSigPreemptCache())
}

// TestApplyPreempt_Armed_PreservesToolUseThinking pins the end-to-end contract:
// even when the per-account preempt is armed (300s cooldown), a request whose
// assistant turn contains tool_use must NOT have its thinking blocks stripped —
// stripping there orphans the tool_use and makes Claude Code report malformed
// tool calls (live edge-us1: 139 signed thinking + 216 tool_use forwarded as 0).
// Complements TestApplyPreempt_Armed_StripsThinkingBlocks (the no-tool case that
// is still safely stripped).
func TestApplyPreempt_Armed_PreservesToolUseThinking(t *testing.T) {
	cache := &fakeSigPreemptCache{}
	cache.armed.Store(true)
	s := &GatewayService{tkAnthropicSigPreemptCache: cache}
	c := newTestGin()

	body := []byte(`{"model":"claude-opus-4-1","thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"tool reasoning","signature":"sigTOOL"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}]}`)

	out := s.applySigPreemptIfArmed(context.Background(), c, newTestAccount(), body)

	// Tool-coupled thinking + its signature must pass through verbatim.
	require.Contains(t, string(out), `"type":"thinking"`, "tool-coupled thinking must NOT be stripped while preempt is armed")
	require.Contains(t, string(out), "sigTOOL", "signature must pass through verbatim")
	require.Contains(t, string(out), `"type":"tool_use"`, "tool_use must remain")
	// Top-level thinking must stay enabled (a preserved thinking block requires it).
	require.Contains(t, string(out), `"thinking":{"type":"enabled"}`, "top-level thinking must stay enabled")
	// Body unchanged → no signature_preempt_applied event (armed-but-nothing-to-strip stays silent).
	require.False(t, hasOpsEventKind(c, "signature_preempt_applied"), "no applied event when nothing is safely strippable")
	require.True(t, bytes.Equal(out, body), "armed preempt must be a no-op for a tool-coupled thinking request")
}
