//go:build unit

package service

// Unit tests for tokensea pre-send strips (context_management + cache_control.scope).
// Owner: gateway_request_tk_tokensea.go. Account-gated, not model-gated.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func tokenseaNewAPIAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Name:     "tokensea/anthropic",
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai",
			"api_key":  "sk-test",
		},
	}
}

// TestTkStripTokenseaContextManagement_StripsOnTokenseaModels pins that
// tokensea rejects context_management for every Claude model we have seen in
// prod (fable-5, opus-4-8, opus-5) — not only Fable.
func TestTkStripTokenseaContextManagement_StripsOnTokenseaModels(t *testing.T) {
	account := tokenseaNewAPIAccount(136)
	models := []string{"claude-fable-5", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-4-6"}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","thinking":{"type":"adaptive"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)
			got := tkStripTokenseaContextManagement(account, body)
			require.False(t, gjson.GetBytes(got, "context_management").Exists())
			require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
			require.Equal(t, model, gjson.GetBytes(got, "model").String())
			require.True(t, gjson.GetBytes(got, "messages").Exists())
		})
	}
}

func TestTkStripTokenseaContextManagement_NoTouch(t *testing.T) {
	tokensea := tokenseaNewAPIAccount(136)
	cursor := &Account{
		ID:       150,
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agentn.global.api5.cursor.sh",
			"api_key":  "sk-test",
		},
		Extra: map[string]any{"upstream_provider": "cursor"},
	}
	cmBody := `{"model":"claude-fable-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"max_tokens":100}`
	opusCM := `{"model":"claude-opus-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"max_tokens":100}`
	noCM := `{"model":"claude-fable-5","thinking":{"type":"adaptive"},"max_tokens":100}`

	t.Run("cursor+fable keeps CM", func(t *testing.T) {
		got := tkStripTokenseaContextManagement(cursor, []byte(cmBody))
		require.Equal(t, cmBody, string(got))
	})
	t.Run("cursor+opus keeps CM", func(t *testing.T) {
		got := tkStripTokenseaContextManagement(cursor, []byte(opusCM))
		require.Equal(t, opusCM, string(got))
	})
	t.Run("tokensea without CM is no-op", func(t *testing.T) {
		got := tkStripTokenseaContextManagement(tokensea, []byte(noCM))
		require.Equal(t, noCM, string(got))
	})
	t.Run("nil account keeps CM", func(t *testing.T) {
		got := tkStripTokenseaContextManagement(nil, []byte(cmBody))
		require.Equal(t, cmBody, string(got))
	})
}

func TestTkStripTokenseaContextManagement_OpenAIAndAnthropicTypedRelays(t *testing.T) {
	cmBody := []byte(`{"model":"claude-opus-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"max_tokens":32}`)
	openaiRelay := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai/v1"},
	}
	anthropicRelay := &Account{
		Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai"},
	}
	for _, account := range []*Account{openaiRelay, anthropicRelay} {
		got := tkStripTokenseaContextManagement(account, cmBody)
		require.False(t, gjson.GetBytes(got, "context_management").Exists())
	}
}

func TestIsTokenseaRelayUpstream_NewAPIChannel(t *testing.T) {
	require.True(t, isTokenseaRelayUpstream(tokenseaNewAPIAccount(136)))
	require.False(t, isTokenseaRelayUpstream(&Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agentn.global.api5.cursor.sh"},
	}))
}

// Prod 2026-09-28 user 16: claude-opus-5 → account 136 (tokensea) 400
// "system.3.cache_control.***.scope: Extra inputs are not permitted".
func TestTkStripTokenseaCacheControlScope_StripsSystemMessagesTools(t *testing.T) {
	account := tokenseaNewAPIAccount(136)
	body := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"billing"},{"type":"text","text":"identity"},{"type":"text","text":"expansion","cache_control":{"type":"ephemeral","ttl":"5m"}},{"type":"text","text":"project","cache_control":{"type":"ephemeral","ttl":"1h","scope":"org"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral","scope":"org"}}]}],"tools":[{"name":"lookup","input_schema":{},"cache_control":{"type":"ephemeral","scope":"org"}}],"max_tokens":32}`)

	got := tkStripTokenseaCacheControlScope(account, body)
	require.False(t, gjson.GetBytes(got, "system.3.cache_control.scope").Exists())
	require.Equal(t, "ephemeral", gjson.GetBytes(got, "system.3.cache_control.type").String())
	require.Equal(t, "1h", gjson.GetBytes(got, "system.3.cache_control.ttl").String())
	require.False(t, gjson.GetBytes(got, "system.2.cache_control.scope").Exists())
	require.Equal(t, "5m", gjson.GetBytes(got, "system.2.cache_control.ttl").String())
	require.False(t, gjson.GetBytes(got, "messages.0.content.0.cache_control.scope").Exists())
	require.Equal(t, "ephemeral", gjson.GetBytes(got, "messages.0.content.0.cache_control.type").String())
	require.False(t, gjson.GetBytes(got, "tools.0.cache_control.scope").Exists())
	require.Equal(t, "ephemeral", gjson.GetBytes(got, "tools.0.cache_control.type").String())
	require.Equal(t, "claude-opus-5", gjson.GetBytes(got, "model").String())
	require.Equal(t, "project", gjson.GetBytes(got, "system.3.text").String())
}

func TestTkStripTokenseaCacheControlScope_NoTouch(t *testing.T) {
	tokensea := tokenseaNewAPIAccount(136)
	cursor := &Account{
		ID:       150,
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agentn.global.api5.cursor.sh",
			"api_key":  "sk-test",
		},
		Extra: map[string]any{"upstream_provider": "cursor"},
	}
	scoped := `{"model":"claude-opus-5","system":[{"type":"text","text":"project","cache_control":{"type":"ephemeral","scope":"org"}}],"max_tokens":32}`
	noScope := `{"model":"claude-opus-5","system":[{"type":"text","text":"project","cache_control":{"type":"ephemeral","ttl":"1h"}}],"max_tokens":32}`

	t.Run("cursor keeps scope", func(t *testing.T) {
		got := tkStripTokenseaCacheControlScope(cursor, []byte(scoped))
		require.Equal(t, scoped, string(got))
	})
	t.Run("nil account keeps scope", func(t *testing.T) {
		got := tkStripTokenseaCacheControlScope(nil, []byte(scoped))
		require.Equal(t, scoped, string(got))
	})
	t.Run("tokensea without scope is no-op", func(t *testing.T) {
		got := tkStripTokenseaCacheControlScope(tokensea, []byte(noScope))
		require.Equal(t, noScope, string(got))
	})
}

func TestTkStripTokenseaCacheControlScope_OpenAIAndAnthropicTypedRelays(t *testing.T) {
	scoped := []byte(`{"model":"claude-opus-5","system":[{"type":"text","text":"project","cache_control":{"type":"ephemeral","scope":"org"}}],"max_tokens":32}`)
	openaiRelay := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai/v1"},
	}
	anthropicRelay := &Account{
		Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai"},
	}
	for _, account := range []*Account{openaiRelay, anthropicRelay} {
		got := tkStripTokenseaCacheControlScope(account, scoped)
		require.False(t, gjson.GetBytes(got, "system.0.cache_control.scope").Exists())
		require.Equal(t, "ephemeral", gjson.GetBytes(got, "system.0.cache_control.type").String())
	}
}
