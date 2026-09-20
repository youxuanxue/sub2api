//go:build unit

package service

// Unit tests for tkStripFableDisabledThinking — the proactive pre-send strip of
// an explicit `thinking:{"type":"disabled"}` for Fable-tier models.
//
// Evidence (prod, 2026-06-10, user 16, claude-fable-5): upstream rejected the
// explicit disabled shape with a 400 whose message begins:
//
//	"thinking.type.disabled" is not supported for this model. Thinking
//	defaults to adaptive mode and "output_config.effort" to control thinking
//	behavior.
//
// Same-window fable requests with adaptive thinking or no thinking field all
// succeeded. Opus 4.7+ still accepts explicit disabled, hence the gate is
// isFableModel, not requiresAdaptiveOnlyThinking. The bedrock path already
// strips this shape (bedrock_request.go sanitizeBedrockThinking "disabled"
// case); these tests lock the same semantics on the direct Anthropic path.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// TestTkStripFableDisabledThinking_StripIsSurgical asserts that for
// fable+disabled the thinking member disappears and every other byte of the
// body is preserved verbatim (sjson surgical delete, no reformat).
func TestTkStripFableDisabledThinking_StripIsSurgical(t *testing.T) {
	input := []byte(`{"model":"claude-fable-5","max_tokens":32000,"thinking":{"type":"disabled"},"metadata":{"user_id":"u-16"},"messages":[{"role":"user","content":"hi  é"}],"stream":true}`)
	want := []byte(`{"model":"claude-fable-5","max_tokens":32000,"metadata":{"user_id":"u-16"},"messages":[{"role":"user","content":"hi  é"}],"stream":true}`)

	got := tkStripFableDisabledThinking(input)
	require.False(t, gjson.GetBytes(got, "thinking").Exists())
	require.Equal(t, string(want), string(got))
}

// TestTkStripFableDisabledThinking_NoTouch asserts the function returns the
// input bytes unchanged for every shape that must NOT be stripped.
func TestTkStripFableDisabledThinking_NoTouch(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"fable adaptive", `{"model":"claude-fable-5","thinking":{"type":"adaptive"},"max_tokens":100}`},
		{"fable enabled", `{"model":"claude-fable-5","thinking":{"type":"enabled","budget_tokens":1024},"max_tokens":100}`},
		{"fable no thinking", `{"model":"claude-fable-5","max_tokens":100}`},
		{"opus 4.7 disabled (still accepted upstream)", `{"model":"claude-opus-4-7","thinking":{"type":"disabled"},"max_tokens":100}`},
		{"sonnet 4.6 disabled", `{"model":"claude-sonnet-4-6","thinking":{"type":"disabled"},"max_tokens":100}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(tc.body)
			got := tkStripFableDisabledThinking(input)
			require.Equal(t, tc.body, string(got))
		})
	}
}

// TestTkStripFableDisabledThinking_ModelVariants asserts every fable model-id
// variant observed in the wild hits the strip.
func TestTkStripFableDisabledThinking_ModelVariants(t *testing.T) {
	variants := []string{
		"claude-fable-5",
		"claude-fable-5-20260601",  // dated snapshot
		"claude-fable-5[1m]",       // context-window alias
		"anthropic.claude-fable-5", // bedrock form
	}
	for _, model := range variants {
		t.Run(model, func(t *testing.T) {
			body, err := sjson.Set(`{"model":"","thinking":{"type":"disabled"},"max_tokens":100}`, "model", model)
			require.NoError(t, err)
			got := tkStripFableDisabledThinking([]byte(body))
			require.False(t, gjson.GetBytes(got, "thinking").Exists(), "thinking must be stripped for %s", model)
			require.Equal(t, model, gjson.GetBytes(got, "model").String())
		})
	}
}

// TestTkStripFableDisabledThinking_SanitizeChainShape mirrors the exact
// composition used at all three gateway_service.go pre-send sites:
//
//	tkStripDeprecatedSamplingParams(tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(body, account))))
//
// and proves the composed outbound body carries no thinking member.
func TestTkStripFableDisabledThinking_SanitizeChainShape(t *testing.T) {
	account := &Account{ID: 1, Name: "fable-test", Platform: PlatformAnthropic}
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"disabled"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":""}]}],"max_tokens":100}`)

	out := tkStripDeprecatedSamplingParams(tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(body, account))))

	require.False(t, gjson.GetBytes(out, "thinking").Exists())
	require.Equal(t, "claude-fable-5", gjson.GetBytes(out, "model").String())
	require.True(t, gjson.ValidBytes(out))
}

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

// TestTkStripTokenseaFableContextManagement_StripsOnTokenseaFable pins the
// prod 2026-09-20 user16 failover path: newapi tokensea + claude-fable-5 must
// lose context_management before upstream forward.
func TestTkStripTokenseaFableContextManagement_StripsOnTokenseaFable(t *testing.T) {
	account := tokenseaNewAPIAccount(136)
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"adaptive"},"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[{"role":"user","content":"hi"}],"max_tokens":100}`)

	got := tkStripTokenseaFableContextManagement(account, body)

	require.False(t, gjson.GetBytes(got, "context_management").Exists())
	require.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	require.Equal(t, "claude-fable-5", gjson.GetBytes(got, "model").String())
	require.True(t, gjson.GetBytes(got, "messages").Exists())
}

func TestTkStripTokenseaFableContextManagement_NoTouch(t *testing.T) {
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
	opusBody := `{"model":"claude-opus-4-8","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"max_tokens":100}`
	noCM := `{"model":"claude-fable-5","thinking":{"type":"adaptive"},"max_tokens":100}`

	cases := []struct {
		name    string
		account *Account
		body    string
	}{
		{"cursor+fable keeps CM", cursor, cmBody},
		{"tokensea+opus keeps CM", tokensea, opusBody},
		{"tokensea+fable without CM is no-op", tokensea, noCM},
		{"nil account keeps CM", nil, cmBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tkStripTokenseaFableContextManagement(tc.account, []byte(tc.body))
			require.Equal(t, tc.body, string(got))
		})
	}
}

func TestTkStripTokenseaFableContextManagement_OpenAIAndAnthropicTypedRelays(t *testing.T) {
	cmBody := []byte(`{"model":"claude-fable-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"max_tokens":32}`)
	openaiRelay := &Account{
		Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai/v1"},
	}
	anthropicRelay := &Account{
		Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai"},
	}
	for _, account := range []*Account{openaiRelay, anthropicRelay} {
		got := tkStripTokenseaFableContextManagement(account, cmBody)
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
