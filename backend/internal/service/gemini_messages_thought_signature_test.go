package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertClaudeMessagesToGeminiContents_PropagatesThinkingSignatureToToolUse(t *testing.T) {
	// TK keeps Claude thinking as a Gemini thought part (unlike upstream #8545 which
	// drops thinking and only stamps the next part). The following tool_use without
	// its own signature must still receive the real thoughtSignature, not the dummy.
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "compute 2+2"},
			},
		},
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":      "thinking",
					"thinking":  "I should use the calc tool",
					"signature": "sig_real_from_upstream_999",
				},
				map[string]any{
					"type":  "tool_use",
					"id":    "toolu_123",
					"name":  "calc",
					"input": map[string]any{"expr": "2+2"},
				},
			},
		},
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_123",
					"content":     "4",
				},
			},
		},
	}

	toolMap := make(map[string]string)
	contents, err := convertClaudeMessagesToGeminiContents(messages, toolMap, false)
	require.NoError(t, err)
	require.Len(t, contents, 3)

	modelTurn, ok := contents[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "model", modelTurn["role"])

	parts, ok := modelTurn["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 2)

	thoughtPart, ok := parts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, thoughtPart["thought"])
	assert.Equal(t, "sig_real_from_upstream_999", thoughtPart["thoughtSignature"])

	fnPart, ok := parts[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sig_real_from_upstream_999", fnPart["thoughtSignature"])
	assert.NotEqual(t, geminiDummyThoughtSignature, fnPart["thoughtSignature"])

	fc, ok := fnPart["functionCall"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "calc", fc["name"])
}

func TestConvertClaudeMessagesToGeminiContents_PropagatesThinkingSignatureToText(t *testing.T) {
	// TK keeps the thinking block. The following text part also receives the
	// carried thoughtSignature so Gemini multi-turn continuity is not lost when
	// clients omit a separate thought continuum.
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":      "thinking",
					"thinking":  "Deep reasoning...",
					"signature": "sig_text_thought_777",
				},
				map[string]any{
					"type": "text",
					"text": "The answer is 42.",
				},
			},
		},
	}

	toolMap := make(map[string]string)
	contents, err := convertClaudeMessagesToGeminiContents(messages, toolMap, false)
	require.NoError(t, err)
	require.Len(t, contents, 1)

	modelTurn, ok := contents[0].(map[string]any)
	require.True(t, ok)
	parts, ok := modelTurn["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 2)

	thoughtPart, ok := parts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, thoughtPart["thought"])
	assert.Equal(t, "sig_text_thought_777", thoughtPart["thoughtSignature"])

	textPart, ok := parts[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "The answer is 42.", textPart["text"])
	assert.Equal(t, "sig_text_thought_777", textPart["thoughtSignature"])
}

func TestConvertClaudeMessagesToGeminiContents_PreservesExistingToolUseSignature(t *testing.T) {
	// If tool_use already has signature directly (e.g. from Claude Code), preserve it directly.
	messages := []any{
		map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{
					"type":      "tool_use",
					"id":        "toolu_direct",
					"name":      "calc",
					"input":     map[string]any{"expr": "3*3"},
					"signature": "sig_direct_tool_use_555",
				},
			},
		},
	}

	toolMap := make(map[string]string)
	contents, err := convertClaudeMessagesToGeminiContents(messages, toolMap, false)
	require.NoError(t, err)
	require.Len(t, contents, 1)

	modelTurn, ok := contents[0].(map[string]any)
	require.True(t, ok)
	parts, ok := modelTurn["parts"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, parts)
	fnPart, ok := parts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "sig_direct_tool_use_555", fnPart["thoughtSignature"])
}
