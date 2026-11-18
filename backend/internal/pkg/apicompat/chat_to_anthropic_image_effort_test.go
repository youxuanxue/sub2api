package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatCompletionsToAnthropicRequest_ImageURLAndReasoningEffort(t *testing.T) {
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"claude-opus-5-5",
		"max_tokens":128,
		"reasoning_effort":"high",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"describe"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}},
				{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}
			]
		}]
	}`), &req))
	require.NoError(t, ValidateChatToAnthropic(&req))

	out, err := ChatCompletionsToAnthropicRequest(&req)
	require.NoError(t, err)
	require.NotNil(t, out.Thinking)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.NotNil(t, out.OutputConfig)
	require.Equal(t, "high", out.OutputConfig.Effort)
	require.Len(t, out.Messages, 1)

	var blocks []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(out.Messages[0].Content, &blocks))
	require.Equal(t, "text", blocks[0].Type)
	require.Equal(t, "describe", blocks[0].Text)
	require.Equal(t, "image", blocks[1].Type)
	require.NotNil(t, blocks[1].Source)
	require.Equal(t, "base64", blocks[1].Source.Type)
	require.Equal(t, "image/png", blocks[1].Source.MediaType)
	require.Equal(t, "AQID", blocks[1].Source.Data)
	require.Equal(t, "image", blocks[2].Type)
	require.NotNil(t, blocks[2].Source)
	require.Equal(t, "url", blocks[2].Source.Type)
	require.Equal(t, "https://example.com/a.png", blocks[2].Source.URL)
}

func TestValidateChatToAnthropic_RejectsThinkingWithReasoningEffort(t *testing.T) {
	req := ChatCompletionsRequest{
		Model:           "claude-opus-5-5",
		ReasoningEffort: "high",
		Thinking:        json.RawMessage(`{"type":"adaptive"}`),
		Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	require.Error(t, ValidateChatToAnthropic(&req))
}

func TestChatCompletionsToAnthropicRequest_StripsOpenAIOnlyExtras(t *testing.T) {
	// Match new-api / LiteLLM: response_format and legacy functions do not block
	// Chat→Messages. Structured output is dropped; legacy functions remap to tools.
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"claude-fable-5-2",
		"stream":false,
		"max_tokens":64,
		"response_format":{"type":"json_object"},
		"functions":[{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],
		"messages":[{"role":"user","content":"hello"}]
	}`), &req))
	require.NoError(t, ValidateChatToAnthropic(&req))
	out, err := ChatCompletionsToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Equal(t, 64, out.MaxTokens)
	require.Len(t, out.Tools, 1)
	require.Equal(t, "lookup", out.Tools[0].Name)
	raw, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "response_format")
	require.NotContains(t, string(raw), "json_object")
}

func TestChatCompletionsToAnthropicRequest_PinsTemperatureWhenThinking(t *testing.T) {
	temp := 0.2
	req := ChatCompletionsRequest{
		Model:           "claude-opus-5-5",
		MaxTokens:       intPtr(128),
		Temperature:     &temp,
		ReasoningEffort: "high",
		Messages:        []ChatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	out, err := ChatCompletionsToAnthropicRequest(&req)
	require.NoError(t, err)
	require.NotNil(t, out.Thinking)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.NotNil(t, out.Temperature)
	require.InDelta(t, 1.0, *out.Temperature, 1e-9)
}
