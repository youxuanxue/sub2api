//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConvertChatCompletionsToGeminiKeepsImageToolsAndSchema(t *testing.T) {
	body := []byte(`{
		"model":"gemini-3.8-flash",
		"max_tokens":128,
		"tool_choice":"required",
		"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"city":{"type":"string"}},"additionalProperties":false}}},
		"messages":[{"role":"system","content":"Be brief."},{"role":"user","content":[{"type":"text","text":"Where?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}],
		"tools":[{"type":"function","function":{"name":"lookup","description":"Look up","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]
	}`)
	out, err := convertChatCompletionsToGeminiGenerateContent(body, "gemini-3.8-flash")
	require.NoError(t, err)
	require.Equal(t, "Be brief.", gjson.GetBytes(out, "systemInstruction.parts.0.text").String())
	require.Equal(t, "image/png", gjson.GetBytes(out, "contents.0.parts.1.inlineData.mimeType").String())
	require.Equal(t, "aGVsbG8=", gjson.GetBytes(out, "contents.0.parts.1.inlineData.data").String())
	require.Equal(t, "lookup", gjson.GetBytes(out, "tools.0.functionDeclarations.0.name").String())
	require.Equal(t, "ANY", gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String())
	require.Equal(t, "application/json", gjson.GetBytes(out, "generationConfig.responseMimeType").String())
	require.False(t, gjson.GetBytes(out, "generationConfig.responseSchema.additionalProperties").Exists())
	require.Equal(t, int64(128), gjson.GetBytes(out, "generationConfig.maxOutputTokens").Int())

	withThinking := applyGeminiCompatThinkingConfig(out, chatReasoningEffortClaudeBody("high"), "gemini-3.8-flash", false)
	require.Equal(t, "high", gjson.GetBytes(withThinking, "generationConfig.thinkingConfig.thinkingLevel").String())
}

func TestConvertChatCompletionsToGeminiRejectsRemoteImageAndMixedTools(t *testing.T) {
	remote := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`)
	_, err := convertChatCompletionsToGeminiGenerateContent(remote, "gemini-3.8-flash")
	require.Error(t, err)

	mixed := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},{"type":"web_search"}]}`)
	_, err = convertChatCompletionsToGeminiGenerateContent(mixed, "gemini-3.8-flash")
	require.Error(t, err)

	stops := []byte(`{"model":"gemini-3.8-flash","stop":["a","b","c","d","e","f"],"messages":[{"role":"user","content":"hi"}]}`)
	_, err = convertChatCompletionsToGeminiGenerateContent(stops, "gemini-3.8-flash")
	require.Error(t, err)
}
