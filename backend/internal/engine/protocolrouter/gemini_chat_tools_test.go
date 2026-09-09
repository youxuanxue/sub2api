package protocolrouter

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPlanChatToGeminiFunctionTools(t *testing.T) {
	const base = `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`
	for _, tc := range []struct {
		name, patch string
		allowed     bool
	}{
		{"default", `{}`, true},
		{"auto", `{"tool_choice":"auto"}`, true},
		{"parallel_allowed", `{"parallel_tool_calls":true}`, true},
		{"none", `{"tool_choice":"none"}`, false},
		{"required", `{"tool_choice":"required"}`, false},
		{"named", `{"tool_choice":{"type":"function","function":{"name":"lookup"}}}`, false},
		{"parallel_disabled", `{"parallel_tool_calls":false}`, false},
		{"strict", `{"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"},"strict":true}}]}`, false},
		{"built_in", `{"tools":[{"type":"web_search"}]}`, false},
		{"reasoning", `{"reasoning_effort":"high"}`, false},
		{"continuation", `{"previous_response_id":"resp_old"}`, false},
		{"cache", `{"prompt_cache_key":"session"}`, false},
		{"format", `{"response_format":{"type":"json_schema"}}`, false},
		{"image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]}`, false},
		{"tool_history", `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`, true},
		{"orphan_output", `{"messages":[{"role":"tool","tool_call_id":"unknown","content":"sunny"}]}`, false},
		{"invalid_arguments", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"bad"}}]}]}`, false},
		{"unknown_content", `{"messages":[{"role":"user","content":[{"type":"future_block"}]}]}`, false},
		{"unanswered_call", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`, false},
		{"reused_call_id", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"first"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"second"}]}`, false},
		{"normalized_id_collision", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}},{"id":"fc_call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"first"},{"role":"tool","tool_call_id":"fc_call_1","content":"second"}]}`, false},
		{"interleaved_result", `{"messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"user","content":"interruption"},{"role":"tool","tool_call_id":"call_1","content":"sunny"}]}`, false},
		{"legacy_forced_choice", `{"function_call":{"name":"lookup"}}`, false},
		{"legacy_functions", `{"functions":[{"name":"extra","parameters":{"type":"object"}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body, patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(base), &body); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.patch), &patch); err != nil {
				t.Fatal(err)
			}
			for k, v := range patch {
				body[k] = v
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			req, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "gemini-3.8-flash", true, raw)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := New(allTestAdapters()).Plan(req, testAccount(t, ProtocolGeminiGenerateContent))
			if tc.allowed {
				if err != nil || plan.AdapterID() != AdapterChatToGemini {
					t.Fatalf("plan=%+v err=%v", plan, err)
				}
			} else if !errors.Is(err, ErrNoLegalRoute) {
				t.Fatalf("want no legal route, got %v", err)
			}
		})
	}
}
