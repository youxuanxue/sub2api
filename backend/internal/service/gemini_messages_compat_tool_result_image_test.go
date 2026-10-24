package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConvertClaudeMessages_ToolResultKeepsBase64Image(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_1",
					"content": []any{
						map[string]any{"type": "text", "text": "see this"},
						map[string]any{
							"type": "image",
							"source": map[string]any{
								"type":       "base64",
								"media_type": "image/png",
								"data":       "aGVsbG8=",
							},
						},
					},
				},
			},
		},
	}

	out, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{"toolu_1": "view"})
	require.NoError(t, err)
	require.Len(t, out, 1)
	msg, ok := out[0].(map[string]any)
	require.True(t, ok)
	parts, ok := msg["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 2)
	part0, ok := parts[0].(map[string]any)
	require.True(t, ok)
	response, ok := part0["functionResponse"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "view", response["name"])
	payload, ok := response["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "see this", payload["content"])
	part1, ok := parts[1].(map[string]any)
	require.True(t, ok)
	inline, ok := part1["inlineData"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "image/png", inline["mimeType"])
	require.Equal(t, "aGVsbG8=", inline["data"])
}

func TestConvertClaudeMessages_ToolResultDropsNonBase64Image(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "toolu_2",
					"content": []any{
						map[string]any{"type": "text", "text": "caption"},
						map[string]any{
							"type": "image",
							"source": map[string]any{
								"type": "url",
								"url":  "https://example.com/a.png",
							},
						},
					},
				},
			},
		},
	}

	out, err := convertClaudeMessagesToGeminiContents(messages, nil)
	require.NoError(t, err)
	msg, ok := out[0].(map[string]any)
	require.True(t, ok)
	parts, ok := msg["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)
	part0, ok := parts[0].(map[string]any)
	require.True(t, ok)
	response, ok := part0["functionResponse"].(map[string]any)
	require.True(t, ok)
	payload, ok := response["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "caption", payload["content"])
	require.NotContains(t, part0, "inlineData")
}
