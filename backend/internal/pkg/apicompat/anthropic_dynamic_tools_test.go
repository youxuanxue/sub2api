//go:build unit

package apicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func dynamicToolRequest(t *testing.T, body string) *AnthropicRequest {
	t.Helper()
	var req AnthropicRequest
	require.NoError(t, json.Unmarshal([]byte(body), &req))
	return &req
}

func TestAnthropicDynamicTools_PreservesOrderAndReferences(t *testing.T) {
	req := dynamicToolRequest(t, `{"model":"gpt-5","tools":[{"name":"base","input_schema":{"type":"object"}},{"name":"late","defer_loading":true,"input_schema":{"type":"object","properties":{"x":{"type":"string"}}}}],"messages":[{"role":"user","content":"start"},{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"base","input":{}}]},{"role":"system","content":[{"type":"text","text":"before"},{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"handback","input_schema":{"type":"object"}}}},{"type":"text","text":"after"},{"type":"tool_addition","tool":{"type":"tool_reference","name":"late"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]},{"role":"user","content":"continue"}]}`)
	before, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := AnthropicToResponses(req)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)
	require.Equal(t, "base", out.Tools[0].Name)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(out.Input, &items))
	require.Len(t, items, 8)
	require.Equal(t, "function_call_output", items[2]["type"])
	require.Equal(t, "developer", items[3]["role"])
	require.Equal(t, "additional_tools", items[4]["type"])
	require.Equal(t, "handback", items[4]["tools"].([]any)[0].(map[string]any)["name"])
	require.Equal(t, "developer", items[5]["role"])
	require.Equal(t, "additional_tools", items[6]["type"])
	require.Equal(t, "late", items[6]["tools"].([]any)[0].(map[string]any)["name"])
	require.Equal(t, "user", items[7]["role"])
	effective, err := EffectiveResponsesTools(out)
	require.NoError(t, err)
	require.Len(t, effective, 3)
	require.Equal(t, "late", effective[2].Name)
	after, err := json.Marshal(req)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestAnthropicDynamicTools_RejectsUnrepresentableChanges(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"string payload", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":"bad"}]}]}`, "invalid"},
		{"array payload", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":[]}]}]}`, "invalid"},
		{"system prompt", `{"system":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late"}}}]}`, "system prompt"},
		{"pending result", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"base","input":{}}]},{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late"}}}]}]}`, "pending"},
		{"removal", `{"messages":[{"role":"system","content":[{"type":"tool_removal","tool":{"type":"tool_reference","name":"base"}}]}]}`, "tool_removal"},
		{"unknown reference", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"missing"}}]}]}`, "unknown"},
		{"missing payload", `{"messages":[{"role":"system","content":[{"type":"tool_addition"}]}]}`, "tool"},
		{"wrong role", `{"messages":[{"role":"user","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late"}}}]}]}`, "system"},
		{"conflict", `{"tools":[{"name":"late","description":"first"}],"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late","description":"other"}}}]}]}`, "conflict"},
		{"ambiguous reference", `{"tools":[{"name":"late"},{"name":"late"}],"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"late"}}]}]}`, "ambiguous"},
		{"invalid schema", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late","input_schema":[]}}}]}]}`, "schema"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := AnthropicToResponses(dynamicToolRequest(t, tc.body))
			require.ErrorContains(t, err, tc.want)
			require.Nil(t, out)
		})
	}
}

func TestAnthropicDynamicTools_LongNameSurvivesTypedInput(t *testing.T) {
	name := "mcp__" + strings.Repeat("long_server_", 8) + "handback"
	body := `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"` + name + `","input_schema":{"type":"object"}}}}]}]}`
	out, err := AnthropicToResponses(dynamicToolRequest(t, body))
	require.NoError(t, err)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(out.Input, &items))
	require.Len(t, items, 1)
	require.Equal(t, name, items[0]["tools"].([]any)[0].(map[string]any)["name"])
}

func TestAnthropicDynamicTools_DirectChatRejectsInsteadOfDropping(t *testing.T) {
	req := dynamicToolRequest(t, `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late"}}}]}]}`)
	out, err := AnthropicToChatCompletionsRequest(req)
	require.ErrorContains(t, err, "Responses bridge")
	require.Nil(t, out)
}
