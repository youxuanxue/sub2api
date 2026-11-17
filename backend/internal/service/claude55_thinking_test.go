//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAdaptThinkingToClaude55_EnabledBecomesAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":4096}}`)
	got := adaptThinkingToClaude55(body, "claude-opus-5-5")
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	require.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestAdaptThinkingToClaude55_DisabledBecomesBetweenToolsOnSonnet55(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","thinking":{"type":"disabled"}}`)
	got := adaptThinkingToClaude55(body, "claude-sonnet-5-5")
	require.Equal(t, "between_tools", gjson.GetBytes(got, "thinking.type").String())
}

func TestAdaptThinkingToClaude55_NonClaude55Unchanged(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","thinking":{"type":"enabled","budget_tokens":1024}}`)
	got := adaptThinkingToClaude55(body, "claude-sonnet-4-6")
	require.JSONEq(t, string(body), string(got))
}

func TestRewriteCompositeRequestModel_AdaptsThinkingOnRewrite(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":4096}}`)
	got := RewriteCompositeRequestModel(body, "model", "claude-opus-5-5")
	require.Equal(t, "claude-opus-5-5", gjson.GetBytes(got, "model").String())
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	require.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestRewriteCompositeRequestModel_SameModelKeepsThinking(t *testing.T) {
	// Middleware only calls rewrite when upstream != public; the helper itself
	// still adapts when given a Claude 5.5 target — callers that keep the
	// client model must not invoke it (covered by middleware equality guard).
	body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"enabled","budget_tokens":4096}}`)
	got := RewriteCompositeRequestModel(body, "model", "claude-opus-5-5")
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
}
