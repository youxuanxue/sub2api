package service

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
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

	out, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{"toolu_1": "view"}, false)
	require.NoError(t, err)
	require.Len(t, out, 1)
	msg, ok := out[0].(map[string]any)
	require.True(t, ok)
	parts, ok := msg["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)
	response := claudeToolFunctionResponse(t, parts[0])
	require.Equal(t, "view", response["name"])
	payload, ok := response["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "see this", payload["content"])
	inline := claudeToolInlineData(t, response, 0)
	require.Equal(t, "image/png", inline["mimeType"])
	require.Equal(t, "aGVsbG8=", inline["data"])
	require.NotContains(t, inline, "mime_type")
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

	out, err := convertClaudeMessagesToGeminiContents(messages, nil, false)
	require.NoError(t, err)
	msg, ok := out[0].(map[string]any)
	require.True(t, ok)
	parts, ok := msg["parts"].([]any)
	require.True(t, ok)
	require.Len(t, parts, 1)
	response := claudeToolFunctionResponse(t, parts[0])
	payload, ok := response["response"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "caption", payload["content"])
	require.NotContains(t, response, "parts")
}

func TestConvertClaudeMessages_ToolResultImagesEnterOwningFunctionResponse(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "before"},
				claudeToolResult("toolu_a", []any{
					map[string]any{"type": "text", "text": "shot"},
					claudeBase64Image("image/png", "png-a"),
					claudeBase64Image("image/jpeg", "jpeg-a"),
				}),
				claudeToolResult("toolu_b", []any{
					claudeBase64Image("image/webp", "webp-b"),
				}),
				claudeToolResult("toolu_c", []any{
					map[string]any{"type": "text", "text": "plain"},
				}),
				map[string]any{"type": "text", "text": "after"},
			},
		},
	}

	out, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{
		"toolu_a": "read_a",
		"toolu_b": "read_b",
		"toolu_c": "read_c",
	}, false)
	require.NoError(t, err)
	parts := claudeGeminiParts(t, out)

	require.Equal(t, []any{map[string]any{"text": "before"}}, []any{parts[0]})
	require.NotContains(t, parts[0], "functionResponse")

	first := claudeToolFunctionResponse(t, parts[1])
	require.Equal(t, "read_a", first["name"])
	require.Equal(t, map[string]any{"content": "shot"}, first["response"])
	require.Equal(t, map[string]any{"mimeType": "image/png", "data": "png-a"}, claudeToolInlineData(t, first, 0))
	require.Equal(t, map[string]any{"mimeType": "image/jpeg", "data": "jpeg-a"}, claudeToolInlineData(t, first, 1))

	second := claudeToolFunctionResponse(t, parts[2])
	require.Equal(t, "read_b", second["name"])
	require.Equal(t, map[string]any{"content": "[image attached below]"}, second["response"])
	require.Equal(t, map[string]any{"mimeType": "image/webp", "data": "webp-b"}, claudeToolInlineData(t, second, 0))

	third := claudeToolFunctionResponse(t, parts[3])
	require.Equal(t, "read_c", third["name"])
	require.Equal(t, map[string]any{"content": "plain"}, third["response"])
	require.NotContains(t, third, "parts")

	require.Equal(t, map[string]any{"text": "after"}, parts[4])
	require.NotContains(t, parts[4], "functionResponse")
	require.Len(t, parts, 5)
}

func TestConvertClaudeMessages_ToolResultWithoutImageOmitsParts(t *testing.T) {
	messages := []any{
		map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "look"},
				claudeToolResult("toolu_plain", "only text"),
				map[string]any{"type": "text", "text": "next"},
			},
		},
	}

	out, err := convertClaudeMessagesToGeminiContents(messages, map[string]string{"toolu_plain": "read_plain"}, false)
	require.NoError(t, err)
	parts := claudeGeminiParts(t, out)
	require.Equal(t, []any{
		map[string]any{"text": "look"},
		map[string]any{"functionResponse": map[string]any{
			"name":     "read_plain",
			"response": map[string]any{"content": "only text"},
		}},
		map[string]any{"text": "next"},
	}, parts)
}

func TestAntigravityToolResultImagesEnterOwningFunctionResponse(t *testing.T) {
	body := transformAntigravityToolResults(t)

	const base = "request.contents.1.parts"
	require.Equal(t, int64(5), gjson.GetBytes(body, base+".#").Int())
	require.Equal(t, "before", gjson.GetBytes(body, base+".0.text").String())
	require.False(t, gjson.GetBytes(body, base+".0.functionResponse").Exists())

	require.Equal(t, "read_a", gjson.GetBytes(body, base+".1.functionResponse.name").String())
	require.Equal(t, "shot", gjson.GetBytes(body, base+".1.functionResponse.response.result").String())
	require.Equal(t, "image/png", gjson.GetBytes(body, base+".1.functionResponse.parts.0.inlineData.mimeType").String())
	require.Equal(t, "png-a", gjson.GetBytes(body, base+".1.functionResponse.parts.0.inlineData.data").String())
	require.Equal(t, "image/jpeg", gjson.GetBytes(body, base+".1.functionResponse.parts.1.inlineData.mimeType").String())
	require.Equal(t, "jpeg-a", gjson.GetBytes(body, base+".1.functionResponse.parts.1.inlineData.data").String())
	require.False(t, gjson.GetBytes(body, base+".1.inlineData").Exists())
	require.False(t, gjson.GetBytes(body, base+".1.functionResponse.parts.0.text").Exists())

	require.Equal(t, "read_b", gjson.GetBytes(body, base+".2.functionResponse.name").String())
	require.Equal(t, "Command executed successfully.", gjson.GetBytes(body, base+".2.functionResponse.response.result").String())
	require.Equal(t, "image/webp", gjson.GetBytes(body, base+".2.functionResponse.parts.0.inlineData.mimeType").String())
	require.Equal(t, "webp-b", gjson.GetBytes(body, base+".2.functionResponse.parts.0.inlineData.data").String())
	require.False(t, gjson.GetBytes(body, base+".2.inlineData").Exists())
	require.NotContains(t, string(body), `"mime_type"`)
}

func TestAntigravityToolResultWithoutImageOmitsParts(t *testing.T) {
	body := transformAntigravityToolResults(t)

	const base = "request.contents.1.parts"
	require.Equal(t, "read_c", gjson.GetBytes(body, base+".3.functionResponse.name").String())
	require.Equal(t, "plain", gjson.GetBytes(body, base+".3.functionResponse.response.result").String())
	require.False(t, gjson.GetBytes(body, base+".3.functionResponse.parts").Exists())
	require.False(t, gjson.GetBytes(body, base+".3.inlineData").Exists())
	require.Equal(t, "after", gjson.GetBytes(body, base+".4.text").String())
	require.False(t, gjson.GetBytes(body, base+".4.functionResponse").Exists())
	require.Equal(t, "png-a", gjson.GetBytes(body, base+".1.functionResponse.parts.0.inlineData.data").String())
	require.False(t, gjson.GetBytes(body, base+".3.functionResponse.parts.0.inlineData.data").Exists())
}

func transformAntigravityToolResults(t *testing.T) []byte {
	t.Helper()
	claudeReq := &antigravity.ClaudeRequest{
		Model: "claude-sonnet-4-5",
		Messages: []antigravity.ClaudeMessage{
			{
				Role: "assistant",
				Content: json.RawMessage(`[
					{"type":"tool_use","id":"toolu_a","name":"read_a","input":{}},
					{"type":"tool_use","id":"toolu_b","name":"read_b","input":{}},
					{"type":"tool_use","id":"toolu_c","name":"read_c","input":{}}
				]`),
			},
			{
				Role: "user",
				Content: json.RawMessage(`[
					{"type":"text","text":"before"},
					{"type":"tool_result","tool_use_id":"toolu_a","content":[
						{"type":"text","text":"shot"},
						{"type":"image","source":{"type":"base64","media_type":"image/png","data":"png-a"}},
						{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"jpeg-a"}}
					]},
					{"type":"tool_result","tool_use_id":"toolu_b","content":[
						{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"webp-b"}}
					]},
					{"type":"tool_result","tool_use_id":"toolu_c","content":[
						{"type":"text","text":"plain"}
					]},
					{"type":"text","text":"after"}
				]`),
			},
		},
	}
	body, err := antigravity.TransformClaudeToGeminiWithOptions(claudeReq, "project-1", "claude-sonnet-4-5", antigravity.DefaultTransformOptions())
	require.NoError(t, err)
	return body
}

func claudeGeminiParts(t *testing.T, out []any) []any {
	t.Helper()
	require.Len(t, out, 1)
	msg, ok := out[0].(map[string]any)
	require.True(t, ok)
	parts, ok := msg["parts"].([]any)
	require.True(t, ok)
	return parts
}

func claudeToolFunctionResponse(t *testing.T, part any) map[string]any {
	t.Helper()
	pm, ok := part.(map[string]any)
	require.True(t, ok)
	require.NotContains(t, pm, "inlineData")
	response, ok := pm["functionResponse"].(map[string]any)
	require.True(t, ok)
	return response
}

func claudeToolInlineData(t *testing.T, response map[string]any, index int) map[string]any {
	t.Helper()
	media, ok := response["parts"].([]any)
	require.True(t, ok)
	require.Greater(t, len(media), index)
	imagePart, ok := media[index].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, imagePart, "text")
	inline, ok := imagePart["inlineData"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, inline, "mime_type")
	return inline
}

func claudeToolResult(id string, content any) map[string]any {
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": id,
		"content":     content,
	}
}

func claudeBase64Image(mediaType, data string) map[string]any {
	return map[string]any{
		"type": "image",
		"source": map[string]any{
			"type":       "base64",
			"media_type": mediaType,
			"data":       data,
		},
	}
}
