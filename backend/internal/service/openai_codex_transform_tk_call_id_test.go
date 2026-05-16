package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression pin for Wei-Shaw/sub2api#2500.
//
// `fixCallIDPrefix` in openai_codex_transform.go normalizes OpenAI-format
// `call_<nanoid>` tool-call ids into codex's `fc_<nanoid>` form. The codex
// backend's id validator requires the underscore separator; without it the
// upstream returns HTTP 400 ("Expected an ID that contains letters, numbers,
// underscores, or dashes, but this value contained additional characters")
// and sub2api surfaces that as 502 on every multi-hop turn after the first
// tool call. The bug was a copy-paste slip in the `call_` branch (the
// fallback branch was already correct).

func TestApplyCodexOAuthTransform_FunctionCallNanoidIDIsCodexFcUnderscore(t *testing.T) {
	// Mirrors the real-world id shape from upstream issue evidence:
	// `call_YYen1qxDejd2myJwcTCf7Nyp` must become `fc_YYen1qxDejd2myJwcTCf7Nyp`,
	// NOT `fcYYen1qxDejd2myJwcTCf7Nyp` (which the codex backend rejects with 400).
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_YYen1qxDejd2myJwcTCf7Nyp",
				"name":      "shell",
				"arguments": "{}",
			},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_YYen1qxDejd2myJwcTCf7Nyp", item["call_id"], "call_ prefix must be rewritten to fc_ (with underscore), see Wei-Shaw/sub2api#2500")
}

func TestApplyCodexOAuthTransform_ItemReferenceCallIDIsCodexFcUnderscore(t *testing.T) {
	// The hop-2 replay path: `item_reference` whose id matches the prior
	// `call_<nanoid>` must produce `fc_<nanoid>` so the codex backend can
	// resolve the reference. This was the exact failure path in the upstream
	// issue's operator forensic data.
	reqBody := map[string]any{
		"model": "gpt-5.2",
		"input": []any{
			map[string]any{"type": "item_reference", "id": "call_YYen1qxDejd2myJwcTCf7Nyp"},
			map[string]any{"type": "function_call_output", "call_id": "call_YYen1qxDejd2myJwcTCf7Nyp", "output": "ok"},
		},
	}

	applyCodexOAuthTransform(reqBody, false, false)

	input, ok := reqBody["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 2)
	first, ok := input[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_YYen1qxDejd2myJwcTCf7Nyp", first["id"], "item_reference id must keep underscore after fc rewrite")
	second, ok := input[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fc_YYen1qxDejd2myJwcTCf7Nyp", second["call_id"])
}
