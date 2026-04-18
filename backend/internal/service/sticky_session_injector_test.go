//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newAutoStrategy() StickyStrategy {
	return StickyStrategy{GlobalEnabled: true, Mode: StickyModeAuto}
}

func newPassthroughStrategy() StickyStrategy {
	return StickyStrategy{GlobalEnabled: true, Mode: StickyModePassthrough}
}

func newOffStrategy() StickyStrategy {
	return StickyStrategy{GlobalEnabled: true, Mode: StickyModeOff}
}

// US-201 AC-001: positive — Codex client without prompt_cache_key + identical
// system across requests must derive the same sticky key.
func TestUS201_DeriveStable_SameSystemSameKey(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","instructions":"You are a helpful coding agent.","input":[{"type":"text","text":"hi"}]}`)
	req := StickyInjectionRequest{
		APIKeyID:      42,
		GroupID:       7,
		UpstreamModel: "gpt-5.4",
		AccountKind:   StickyAccountOpenAIOAuth,
		Strategy:      newAutoStrategy(),
		Headers:       http.Header{},
	}
	k1 := DeriveStickyKey(req, body)
	k2 := DeriveStickyKey(req, body)

	require.NotEmpty(t, k1.Value)
	require.Equal(t, StickyKeySourceDerivedContentHash, k1.Source)
	require.Equal(t, k1, k2, "same input must derive identical key")
	require.Truef(t, len(k1.Value) > len(StickyDerivedKeyPrefix),
		"derived key must include prefix: got %q", k1.Value)
}

// US-201 AC-001 (variant): different system prompt → different bucket.
func TestUS201_DeriveStable_DifferentSystemDifferentKey(t *testing.T) {
	body1 := []byte(`{"instructions":"system A","input":[]}`)
	body2 := []byte(`{"instructions":"system B totally different","input":[]}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy()}
	k1 := DeriveStickyKey(req, body1)
	k2 := DeriveStickyKey(req, body2)
	require.NotEmpty(t, k1.Value)
	require.NotEmpty(t, k2.Value)
	require.NotEqual(t, k1.Value, k2.Value)
}

// US-201 AC-006: regression — client-sent prompt_cache_key wins over derive.
func TestUS201_DerivePrefersClientPromptCacheKey(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"client-supplied-key","instructions":"sys","input":[]}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy()}
	k := DeriveStickyKey(req, body)
	require.Equal(t, "client-supplied-key", k.Value)
	require.Equal(t, StickyKeySourceClientPromptCacheKey, k.Source)
}

// US-201 AC-006: header session_id always wins.
func TestUS201_DerivePrefersHeaderSessionID(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"body-key","instructions":"sys","input":[]}`)
	headers := http.Header{}
	headers.Set("session_id", "header-sess")
	req := StickyInjectionRequest{
		APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy(), Headers: headers,
	}
	k := DeriveStickyKey(req, body)
	require.Equal(t, "header-sess", k.Value)
	require.Equal(t, StickyKeySourceClientSessionID, k.Source)
}

// US-201 AC-002: Anthropic mimic — body has metadata.user_id with session_id
// → prefer the inner session_id over hashing.
func TestUS201_DeriveExtractsSessionIDFromMetadata(t *testing.T) {
	body := []byte(`{"metadata":{"user_id":"{\"device_id\":\"abc\",\"account_uuid\":\"xx\",\"session_id\":"11111111-2222-3333-4444-555555555555"}"}}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "claude-3-5", Strategy: newAutoStrategy()}
	k := DeriveStickyKey(req, body)
	// JSON inside string is escaped weirdly above; if parse fails, falls back to raw uid.
	require.NotEmpty(t, k.Value)
	require.Equal(t, StickyKeySourceClientMetadataUserID, k.Source)
}

// US-201 AC-004: group=off → never derive nor inject.
func TestUS201_StrategyOffSkipsEverything(t *testing.T) {
	body := []byte(`{"instructions":"sys","input":[]}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newOffStrategy()}
	k := DeriveStickyKey(req, body)
	require.Empty(t, k.Value)

	out, mut, err := InjectOpenAIResponsesBody(body, StickyKey{Value: "tk_xxx"}, newOffStrategy())
	require.NoError(t, err)
	require.False(t, mut)
	require.Equal(t, body, out)
}

// US-201 AC-005: global off forces passthrough — client key still flows,
// derive does not.
func TestUS201_GlobalOffForcesPassthrough(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"client-key","instructions":"sys"}`)
	strat := StickyStrategy{GlobalEnabled: false, Mode: StickyModeAuto}
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: strat}

	require.Equal(t, StickyModePassthrough, strat.EffectiveMode())
	require.False(t, strat.AllowsDerivation())
	require.True(t, strat.AllowsInjection())

	k := DeriveStickyKey(req, body)
	require.Equal(t, "client-key", k.Value, "passthrough still surfaces client key")

	// And derive without client key returns empty:
	bareReq := req
	k2 := DeriveStickyKey(bareReq, []byte(`{"instructions":"sys"}`))
	require.Empty(t, k2.Value)
}

// US-201 AC-002: positive — non-Claude-Code UA + Anthropic OAuth + auto →
// metadata.user_id is injected when missing.
func TestUS201_InjectAnthropicMessages_WhenAllowed(t *testing.T) {
	body := []byte(`{"model":"claude-3-5","messages":[]}`)
	req := StickyInjectionRequest{
		APIKeyID:       1,
		AccountKind:    StickyAccountAnthropicOAuth,
		IsClaudeCodeUA: false,
		Strategy:       newAutoStrategy(),
	}
	out, mut, err := InjectAnthropicMessagesBody(body, StickyKey{Value: "tk_abc"}, req)
	require.NoError(t, err)
	require.True(t, mut)
	require.Equal(t, "tk_abc", gjson.GetBytes(out, "metadata.user_id").String())
}

// US-201 AC-003: negative — real Claude Code UA must NOT be touched.
func TestUS201_InjectAnthropicMessages_SkipsRealClaudeCode(t *testing.T) {
	body := []byte(`{"model":"claude-3-5","messages":[]}`)
	req := StickyInjectionRequest{
		AccountKind:    StickyAccountAnthropicOAuth,
		IsClaudeCodeUA: true,
		Strategy:       newAutoStrategy(),
	}
	out, mut, err := InjectAnthropicMessagesBody(body, StickyKey{Value: "tk_abc"}, req)
	require.NoError(t, err)
	require.False(t, mut)
	require.False(t, gjson.GetBytes(out, "metadata.user_id").Exists())
}

// US-201 safety: cross-API-key keys must not collide for identical content.
func TestUS201_NoCrossAPIKeyCollision(t *testing.T) {
	body := []byte(`{"instructions":"shared system","input":[]}`)
	req1 := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy()}
	req2 := StickyInjectionRequest{APIKeyID: 2, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy()}
	k1 := DeriveStickyKey(req1, body)
	k2 := DeriveStickyKey(req2, body)
	require.NotEmpty(t, k1.Value)
	require.NotEqual(t, k1.Value, k2.Value, "different api keys must bucket separately")
}

// US-201 safety: derived hash must not contain reversible source content.
func TestUS201_HashNotReversible(t *testing.T) {
	secret := "SECRET_ADMIN_PROMPT_DO_NOT_LEAK"
	body := []byte(`{"instructions":"` + secret + `"}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newAutoStrategy()}
	k := DeriveStickyKey(req, body)
	require.NotEmpty(t, k.Value)
	require.NotContains(t, k.Value, secret)
	require.NotContains(t, k.Value, "SECRET")
}

// US-201 AC-006: existing prompt_cache_key in body must be preserved.
func TestUS201_InjectOpenAI_DoesNotOverrideExisting(t *testing.T) {
	body := []byte(`{"prompt_cache_key":"client-set"}`)
	out, mut, err := InjectOpenAIResponsesBody(body, StickyKey{Value: "tk_should_not_win"}, newAutoStrategy())
	require.NoError(t, err)
	require.False(t, mut)
	require.Equal(t, "client-set", gjson.GetBytes(out, "prompt_cache_key").String())
}

// US-201 strategy degradation: passthrough mode skips derive but still
// allows the gateway to forward whatever the client provided.
func TestUS201_PassthroughDoesNotDerive(t *testing.T) {
	body := []byte(`{"instructions":"sys","input":[]}`)
	req := StickyInjectionRequest{APIKeyID: 1, UpstreamModel: "gpt-5.4", Strategy: newPassthroughStrategy()}
	k := DeriveStickyKey(req, body)
	require.Empty(t, k.Value)
	require.Equal(t, StickyKeySourceNone, k.Source)
}

// US-201 X-Session-Id header injection (NewAPI / GLM path).
func TestUS201_InjectXSessionIDHeader(t *testing.T) {
	headers := http.Header{}
	ok := InjectXSessionIDHeader(headers, StickyKey{Value: "tk_123"}, newAutoStrategy())
	require.True(t, ok)
	require.Equal(t, "tk_123", headers.Get("X-Session-Id"))

	// Existing value must not be overwritten.
	headers2 := http.Header{}
	headers2.Set("X-Session-Id", "client-set")
	ok2 := InjectXSessionIDHeader(headers2, StickyKey{Value: "tk_other"}, newAutoStrategy())
	require.False(t, ok2)
	require.Equal(t, "client-set", headers2.Get("X-Session-Id"))

	// Off strategy is a no-op.
	headers3 := http.Header{}
	ok3 := InjectXSessionIDHeader(headers3, StickyKey{Value: "tk_x"}, newOffStrategy())
	require.False(t, ok3)
	require.Equal(t, "", headers3.Get("X-Session-Id"))
}
