package protocolrouter

import (
	"errors"
	"testing"
)

func TestPlanMessagesToGeminiFunctionTools(t *testing.T) {
	const base = `"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]`
	for _, tc := range []struct {
		name, extra string
		allowed     bool
	}{
		{"default_auto", "", true},
		{"explicit_auto", `,"tool_choice":{"type":"auto"}`, true},
		{"disabled", `,"tool_choice":{"type":"none"}`, false},
		{"disabled_string", `,"tool_choice":"none"`, false},
		{"forced", `,"tool_choice":{"type":"any"}`, false},
		{"named", `,"tool_choice":{"type":"tool","name":"lookup"}`, false},
		{"parallel_constraint", `,"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, false},
		{"reasoning", `,"thinking":{"type":"enabled","budget_tokens":1024}`, false},
		{"continuation", `,"previous_response_id":"resp_1"`, false},
		{"cache", `,"prompt_cache_key":"session"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := ParseCanonicalRequest(ProtocolMessages, ResponsesPathNone, "gemini-3.8-flash", false, []byte("{"+base+tc.extra+"}"))
			if err != nil {
				t.Fatal(err)
			}
			plan, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent))
			if !tc.allowed {
				if !errors.Is(err, ErrNoLegalRoute) {
					t.Fatalf("error = %v, want no legal route", err)
				}
				return
			}
			if err != nil || plan.AdapterID() != AdapterMessagesToGemini {
				t.Fatalf("plan = %+v, error = %v", plan, err)
			}
		})
	}
}

func TestPlanMessagesToGeminiToolHistory(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		allowed       bool
	}{
		{"tool_result_text", `[{"type":"tool_result","tool_use_id":"call_1","content":"sunny"}]`, true},
		{"tool_result_blocks", `[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"sunny"}]}]`, true},
		{"unknown_block", `[{"type":"future_block","text":"unknown"}]`, false},
		{"image_result", `[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}]}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gemini-3.8-flash","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},{"role":"user","content":` + tc.content + `}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
			request, err := ParseCanonicalRequest(ProtocolMessages, ResponsesPathNone, "gemini-3.8-flash", false, body)
			if err != nil {
				t.Fatal(err)
			}
			_, err = New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent))
			if tc.allowed && err != nil || !tc.allowed && !errors.Is(err, ErrNoLegalRoute) {
				t.Fatalf("allowed=%t error=%v", tc.allowed, err)
			}
		})
	}
}
