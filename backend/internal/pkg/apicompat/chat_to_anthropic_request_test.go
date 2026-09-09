package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChatMessagesPreservesToolsCacheAndHistory(t *testing.T) {
	var req ChatCompletionsRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"model":"claude-fable-5","max_tokens":32,"parallel_tool_calls":false,"tool_choice":{"type":"function","function":{"name":"lookup"}},"stop":["END"],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},"cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[
			{"role":"system","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"second"}]},
			{"role":"user","content":"question"},
			{"role":"assistant","content":[{"type":"text","text":"checking","cache_control":{"type":"ephemeral"}}],"tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"a\"}"}},{"id":"call_b","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]},
			{"role":"tool","tool_call_id":"call_a","content":"result a"},
			{"role":"tool","tool_call_id":"call_b","content":"result b","cache_control":{"type":"ephemeral","ttl":"5m"}},
			{"role":"user","content":"continue"}
		]
	}`), &req))
	require.NoError(t, ValidateChatToAnthropic(&req))
	out, err := ChatCompletionsToAnthropicRequest(&req)
	require.NoError(t, err)
	require.Equal(t, 32, out.MaxTokens)
	require.Equal(t, []string{"END"}, out.StopSeqs)
	require.JSONEq(t, `{"type":"tool","name":"lookup","disable_parallel_tool_use":true}`, string(out.ToolChoice))
	require.Len(t, out.Tools, 1)
	require.Equal(t, "1h", out.Tools[0].CacheControl.TTL)
	require.JSONEq(t, string(req.Tools[0].Function.Parameters), string(out.Tools[0].InputSchema))
	var system []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(out.System, &system))
	require.Len(t, system, 2)
	require.Equal(t, "first", system[0].Text)
	require.Equal(t, "1h", system[0].CacheControl.TTL)
	require.Nil(t, system[1].CacheControl)
	require.Len(t, out.Messages, 3)
	var calls, results []AnthropicContentBlock
	require.NoError(t, json.Unmarshal(out.Messages[1].Content, &calls))
	require.Len(t, calls, 3)
	require.Equal(t, "checking", calls[0].Text)
	require.NotNil(t, calls[0].CacheControl)
	require.JSONEq(t, `{"q":"a"}`, string(calls[1].Input))
	require.JSONEq(t, `{"q":"b"}`, string(calls[2].Input))
	require.NoError(t, json.Unmarshal(out.Messages[2].Content, &results))
	require.Len(t, results, 3)
	require.Equal(t, calls[1].ID, results[0].ToolUseID)
	require.Equal(t, calls[2].ID, results[1].ToolUseID)
	require.Equal(t, "5m", results[1].CacheControl.TTL)
	require.Nil(t, results[2].CacheControl)
	require.Equal(t, "continue", results[2].Text)
}

func TestChatMessagesToolChoiceVariants(t *testing.T) {
	for _, tc := range []struct{ choice, want string }{
		{`"auto"`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{`"required"`, `{"type":"any","disable_parallel_tool_use":true}`},
		{`"none"`, `{"type":"none"}`},
	} {
		t.Run(tc.choice, func(t *testing.T) {
			parallel := false
			req := ChatCompletionsRequest{Model: "claude-fable-5", Messages: []ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}}, Tools: []ChatTool{{Type: "function", Function: &ChatFunction{Name: "lookup"}, CacheControl: &AnthropicCacheControl{Type: "ephemeral"}}}, ToolChoice: json.RawMessage(tc.choice), ParallelToolCalls: &parallel}
			require.NoError(t, ValidateChatToAnthropic(&req))
			out, err := ChatCompletionsToAnthropicRequest(&req)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(out.ToolChoice))
			require.NotNil(t, out.Tools[0].CacheControl)
		})
	}
}
