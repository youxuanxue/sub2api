package apicompat

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestGeminiMessagesRequestPreservesFeatures(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"inspect"},{"inlineData":{"mimeType":"image/png","data":"AQID"}}]},{"role":"model","parts":[{"functionCall":{"name":"echo","args":{"value":"OK"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"echo","response":{"result":"OK"}}}]}],"tools":[{"functionDeclarations":[{"name":"echo","parameters":{"type":"OBJECT","properties":{"value":{"type":"STRING"}},"required":["value"]}}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}},"generationConfig":{"maxOutputTokens":4096,"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true},"stopSequences":["END"]}}`
	out, err := GeminiToMessagesRequest([]byte(body), "claude-opus-4-6", true)
	require.NoError(t, err)
	require.True(t, out.Stream)
	require.Equal(t, 1024, out.Thinking.BudgetTokens)
	require.Equal(t, 4096, out.MaxTokens)
	require.JSONEq(t, `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`, string(out.Tools[0].InputSchema))
	require.JSONEq(t, `{"type":"auto"}`, string(out.ToolChoice))
	var calls, results, first []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(out.Messages[1].Content, &calls))
	require.NoError(t, json.Unmarshal(out.Messages[2].Content, &results))
	require.NoError(t, json.Unmarshal(out.Messages[0].Content, &first))
	require.Equal(t, calls[0].ID, results[0].ToolUseID)
	require.JSONEq(t, `"{\"result\":\"OK\"}"`, string(results[0].Content))
	require.Equal(t, "AQID", first[1].Source.Data)
	require.Equal(t, []string{"END"}, out.StopSeqs)
}
func TestGeminiMessagesRejectsLossyRequests(t *testing.T) {
	for _, extra := range []string{
		`,"cachedContent":"cache"`, `,"safetySettings":[]`, `,"generationConfig":{"candidateCount":2}`, `,"generationConfig":{"temperature":1.5}`,
		`,"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}`, `,"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}`,
		`,"tools":[{"googleSearch":{}}]`, `,"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}`,
	} {
		_, err := GeminiToMessagesRequest([]byte(`{"contents":[{"parts":[{"text":"hello"}]}]`+extra+`}`), "claude", false)
		require.Error(t, err, extra)
	}
	for _, part := range []string{
		`{"inlineData":{"mimeType":"video/mp4","data":"AQID"}}`, `{"fileData":{"fileUri":"https://example.org/image"}}`,
		`{"text":"hi","thoughtSignature":"opaque"}`, `{"text":"hi","inlineData":{"mimeType":"image/png","data":"AQID"}}`,
		`{"functionResponse":{"name":"echo","response":{"value":"OK"}}}`,
	} {
		_, err := GeminiToMessagesRequest([]byte(`{"contents":[{"parts":[`+part+`]}]}`), "claude", false)
		require.Error(t, err, part)
	}
	_, err := GeminiToMessagesRequest([]byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"echo","args":{}}},{"functionCall":{"name":"echo","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"echo","response":{}}}]}]}`), "claude", false)
	require.ErrorContains(t, err, "ambiguous")
}
func TestMessagesGeminiResponseAndSignedThinkingRoundTrip(t *testing.T) {
	body := []byte(`{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"provider-signature"},{"type":"tool_use","id":"call_1","name":"echo","input":{"value":"OK"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5,"output_tokens":6}}`)
	response, err := MessagesToGeminiResponse(body)
	require.NoError(t, err)
	require.Equal(t, 18, response.Usage.Total)
	require.Equal(t, 12, response.Usage.Prompt)
	parts := response.Candidates[0].Content.Parts
	require.True(t, parts[0].Thought)
	require.Equal(t, "call_1", parts[1].Call.ID)
	history := map[string]any{"contents": []any{map[string]any{"role": "model", "parts": parts}, map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"id": "call_1", "name": "echo", "response": map[string]any{"value": "OK"}}}}}}}
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	out, err := GeminiToMessagesRequest(raw, "claude", false)
	require.NoError(t, err)
	require.Contains(t, string(out.Messages[0].Content), `"signature":"provider-signature"`)
	for _, invalid := range []string{`{"error":{"message":"secret"}}`, strings.Replace(string(body), `"tool_use","id"`, `"server_tool_use","id"`, 1)} {
		_, err = MessagesToGeminiResponse([]byte(invalid))
		require.Error(t, err)
	}
}
func TestMessagesGeminiStreamRequiresCompleteSequence(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"type":"message","role":"assistant","content":[],"usage":{"input_tokens":3,"cache_read_input_tokens":2}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"id1","name":"echo","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"value\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"OK\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}
	state := &MessagesToGeminiStream{}
	for i, e := range events {
		out, err := state.Convert([]byte(e))
		require.NoError(t, err)
		if i == 2 {
			require.Equal(t, "hello", *out.Candidates[0].Content.Parts[0].Text)
		}
		if i == 7 {
			require.JSONEq(t, `{"value":"OK"}`, string(out.Candidates[0].Content.Parts[0].Call.Args))
		}
		if i < len(events)-1 {
			require.False(t, state.Done())
		} else {
			require.True(t, state.Done())
			require.Equal(t, 12, out.Usage.Total)
			require.Equal(t, "STOP", out.Candidates[0].FinishReason)
		}
	}
	_, err := state.Convert([]byte(events[0]))
	require.Error(t, err)
	for _, e := range []string{events[2], events[9], `{"type":"error"}`, `not-json`} {
		_, err := (&MessagesToGeminiStream{}).Convert([]byte(e))
		require.Error(t, err)
	}
}

func TestGeminiMessagesForcedToolsAndThinkingDoNotConflict(t *testing.T) {
	body := `{"contents":[{"parts":[{"text":"call echo"}]}],"tools":[{"functionDeclarations":[{"name":"echo"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["echo"]}}}`
	out, err := GeminiToMessagesRequest([]byte(body), "claude", false)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"tool","name":"echo"}`, string(out.ToolChoice))
	combined := strings.TrimSuffix(body, "}") + `,"generationConfig":{"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":true}}}`
	_, err = GeminiToMessagesRequest([]byte(combined), "claude", false)
	require.ErrorContains(t, err, "cannot force tool choice")
	p, err := messagesPart(AnthropicContentBlock{Type: "thinking", Thinking: "unsigned provider reasoning"})
	require.NoError(t, err)
	signature, err := decodeMessagesThoughtSignature(p.Signature)
	require.NoError(t, err)
	require.Empty(t, signature)
}
