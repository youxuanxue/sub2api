package protocolrouter

import (
	"errors"
	"testing"
)

func TestPlanResponsesToGeminiUsesChatAdmission(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","tool_choice":"required","input":[{"role":"user","content":[{"type":"input_text","text":"Where?"},{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}`)
	request, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "gemini-3.8-flash", false, body)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent))
	if err != nil || plan.AdapterID() != AdapterResponsesToGemini {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	remote := []byte(`{"model":"gemini-3.8-flash","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}]}`)
	request, err = ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "gemini-3.8-flash", false, remote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent)); !errors.Is(err, ErrNoLegalRoute) {
		t.Fatalf("remote image err=%v", err)
	}
	skipped := []byte(`{"model":"gemini-3.8-flash","input":[{"type":"web_search_call","id":"ws_1"},{"type":"message","role":"user","content":[{"type":"input_text","text":"Where?"}]}]}`)
	request, err = ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "gemini-3.8-flash", false, skipped)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent)); !errors.Is(err, ErrNoLegalRoute) {
		t.Fatalf("skipped responses item err=%v", err)
	}
}

func TestPlanGeminiIdentityAdmitsInlineImage(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"see"},{"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}}]}]}`)
	request, err := ParseCanonicalRequest(ProtocolGeminiGenerateContent, ResponsesPathNone, "gemini-3.8-flash", false, body)
	if err != nil {
		t.Fatal(err)
	}
	if request.Profile().ContentKinds&ContentImage == 0 {
		t.Fatal("inline image was not classified")
	}
	plan, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent))
	if err != nil || plan.AdapterID() != AdapterGeminiIdentity {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestPlanGeminiIdentityIgnoresSchemaKeysNamedContent(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"write","parameters":{"type":"object","properties":{"content":{"type":"string"}}}}]}]}`),
		[]byte(`{"contents":[{"role":"model","parts":[{"functionCall":{"name":"write","args":{"input":{"type":"string","value":"x"}}}}]}]}`),
	}
	for _, body := range bodies {
		request, err := ParseCanonicalRequest(ProtocolGeminiGenerateContent, ResponsesPathNone, "gemini-3.8-flash", false, body)
		if err != nil {
			t.Fatal(err)
		}
		if request.Profile().ContentKinds&^(ContentText|ContentImage) != 0 {
			t.Fatalf("kinds=%b body=%s", request.Profile().ContentKinds, body)
		}
		plan, err := New(allTestAdapters()).Plan(request, testAccount(t, ProtocolGeminiGenerateContent))
		if err != nil || plan.AdapterID() != AdapterGeminiIdentity {
			t.Fatalf("plan=%+v err=%v body=%s", plan, err, body)
		}
	}
}

func TestPlanMessagesToGeminiFunctionTools(t *testing.T) {
	const base = `"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}]`
	for _, tc := range []struct {
		name, extra string
		allowed     bool
	}{
		{"default_auto", "", true},
		{"explicit_auto", `,"tool_choice":{"type":"auto"}`, true},
		{"disabled", `,"tool_choice":{"type":"none"}`, true},
		{"disabled_string", `,"tool_choice":"none"`, true},
		{"forced", `,"tool_choice":{"type":"any"}`, true},
		{"named", `,"tool_choice":{"type":"tool","name":"lookup"}`, true},
		{"parallel_constraint", `,"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, false},
		{"reasoning", `,"thinking":{"type":"enabled","budget_tokens":1024}`, true},
		{"image", `,"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]`, true},
		{"remote_image", `,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/a.png"}}]}]`, false},
		{"continuation", `,"previous_response_id":"resp_1"`, false},
		{"cache", `,"prompt_cache_key":"session"`, true},
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
