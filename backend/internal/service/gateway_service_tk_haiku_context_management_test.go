//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Regression pin for Wei-Shaw/sub2api#2506.
//
// Anthropic /v1/messages returns HTTP 400 "context_management: Extra inputs are
// not permitted" for claude-haiku-4-5-* even when thinking.type is enabled, so
// normalizeClaudeOAuthRequestBody must not inject context_management for Haiku
// requests. This mirrors the existing Haiku exemption in the anthropic-beta
// header path (FullClaudeCodeMimicryBetas) at the call site in
// gateway_service.go.

func TestNormalizeClaudeOAuthRequestBody_Haiku45_ThinkingEnabled_DoesNotInjectContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[]}`)

	out, modelID := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})

	require.True(t, strings.Contains(strings.ToLower(modelID), "haiku"), "model id should still contain haiku after normalization")
	require.False(t, gjson.GetBytes(out, "context_management").Exists(), "Haiku requests must not have context_management auto-injected (Anthropic returns 400)")
}

func TestNormalizeClaudeOAuthRequestBody_Haiku45Dated_ThinkingAdaptive_DoesNotInjectContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5-20251001","thinking":{"type":"adaptive"},"messages":[]}`)

	out, modelID := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5-20251001", claudeOAuthNormalizeOptions{})

	require.Equal(t, "claude-haiku-4-5-20251001", modelID)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(), "Haiku adaptive thinking must not have context_management auto-injected")
}

func TestNormalizeClaudeOAuthRequestBody_Haiku45_ExplicitContextManagement_PreservedWhenClientProvided(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"context_management":{"edits":[{"type":"custom_edit"}]},"messages":[]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})

	cm := gjson.GetBytes(out, "context_management")
	require.True(t, cm.Exists(), "client-provided context_management must be preserved on Haiku")
	require.Equal(t, "custom_edit", gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestNormalizeClaudeOAuthRequestBody_Sonnet_ThinkingEnabled_InjectsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"type":"enabled"},"messages":[]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-5", claudeOAuthNormalizeOptions{})

	cm := gjson.GetBytes(out, "context_management")
	require.True(t, cm.Exists(), "non-Haiku models with thinking enabled must still receive context_management auto-injection (real CLI behavior)")
	require.Equal(t, "clear_thinking_20251015", gjson.GetBytes(out, "context_management.edits.0.type").String())
	require.Equal(t, "all", gjson.GetBytes(out, "context_management.edits.0.keep").String())
}

func TestNormalizeClaudeOAuthRequestBody_Opus_ThinkingEnabled_InjectsContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-5","thinking":{"type":"enabled"},"messages":[]}`)

	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-4-5", claudeOAuthNormalizeOptions{})

	require.True(t, gjson.GetBytes(out, "context_management").Exists(), "Opus must still receive context_management auto-injection")
}
