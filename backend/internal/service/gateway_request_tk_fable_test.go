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
//	tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(body, account)))
//
// and proves the composed outbound body carries no thinking member.
func TestTkStripFableDisabledThinking_SanitizeChainShape(t *testing.T) {
	account := &Account{ID: 1, Name: "fable-test", Platform: PlatformAnthropic}
	body := []byte(`{"model":"claude-fable-5","thinking":{"type":"disabled"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":""}]}],"max_tokens":100}`)

	out := tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(body, account)))

	require.False(t, gjson.GetBytes(out, "thinking").Exists())
	require.Equal(t, "claude-fable-5", gjson.GetBytes(out, "model").String())
	require.True(t, gjson.ValidBytes(out))
}
