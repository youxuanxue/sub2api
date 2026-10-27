package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

// Anthropic→Chat bridge used to splice json.RawMessage tool schemas /
// metadata / tool_use.input verbatim, which preserves duplicate object keys.
// Canonicalize so egress JSON is deny-duplicate-safe.

func TestAnthropicToChatCompletionsBody_CanonicalizesDuplicateJSONKeys(t *testing.T) {
	dupSchema := json.RawMessage(
		`{"type":"object","type":"object","properties":{"command":{"type":"string","type":"string"}},"required":["command"]}`,
	)
	if !strings.Contains(string(dupSchema), `"type":"object","type":"object"`) {
		t.Fatal("fixture must embed duplicate keys before conversion")
	}
	dupMeta := json.RawMessage(`{"user_id":"first","user_id":"last"}`)
	if !strings.Contains(string(dupMeta), `"user_id":"first","user_id":"last"`) {
		t.Fatal("metadata fixture must embed duplicate keys")
	}
	dupInput := `{"command":"ls","command":"pwd"}`
	if !strings.Contains(dupInput, `"command":"ls","command":"pwd"`) {
		t.Fatal("tool_use input fixture must embed duplicate keys")
	}

	req := &apicompat.AnthropicRequest{
		Model:     "gpt-5.6-luna",
		MaxTokens: 32,
		Metadata:  dupMeta,
		Tools: []apicompat.AnthropicTool{{
			Name:        "Bash",
			Description: "run shell",
			InputSchema: dupSchema,
		}},
		Messages: []apicompat.AnthropicMessage{{
			Role: "assistant",
			Content: json.RawMessage(
				`[{"type":"tool_use","id":"call_1","name":"Bash","input":` + dupInput + `}]`,
			),
		}},
	}

	out, err := anthropicToChatCompletionsBody(req, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("anthropicToChatCompletionsBody: %v", err)
	}

	if strings.Contains(string(out), `"type":"object","type":"object"`) {
		t.Fatalf("tool parameters still carry duplicate type keys: %s", out)
	}
	if strings.Contains(string(out), `"user_id":"first","user_id":"last"`) {
		t.Fatalf("metadata still carries duplicate user_id: %s", out)
	}
	if strings.Contains(string(out), `"command":"ls","command":"pwd"`) {
		t.Fatalf("tool_call arguments still carry duplicate command: %s", out)
	}

	assertJSONEq(t, `{"user_id":"last"}`, gjson.GetBytes(out, "metadata").Raw)
	assertJSONEq(t,
		`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`,
		gjson.GetBytes(out, "tools.0.function.parameters").Raw,
	)
	args := gjson.GetBytes(out, "messages.#(role==\"assistant\").tool_calls.0.function.arguments").String()
	assertJSONEq(t, `{"command":"pwd"}`, args)
}

func TestAnthropicToChatCompletionsBody_CleanSchemaPreserved(t *testing.T) {
	req := &apicompat.AnthropicRequest{
		Model:     "gpt-5.6-luna",
		MaxTokens: 16,
		Metadata:  json.RawMessage(`{"user_id":"session-1"}`),
		Tools: []apicompat.AnthropicTool{{
			Name:        "Read",
			Description: "read a file",
			InputSchema: json.RawMessage(
				`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
			),
		}},
		Messages: []apicompat.AnthropicMessage{{
			Role:    "user",
			Content: json.RawMessage(`"hello"`),
		}},
	}

	out, err := anthropicToChatCompletionsBody(req, "gpt-5.6-luna")
	if err != nil {
		t.Fatalf("anthropicToChatCompletionsBody: %v", err)
	}
	if got := gjson.GetBytes(out, "tools.0.function.name").String(); got != "Read" {
		t.Fatalf("tool name=%q want Read", got)
	}
	assertJSONEq(t, `{"user_id":"session-1"}`, gjson.GetBytes(out, "metadata").Raw)
	assertJSONEq(t,
		`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
		gjson.GetBytes(out, "tools.0.function.parameters").Raw,
	)
	if got := gjson.GetBytes(out, "messages.#(role==\"user\").content").String(); got != "hello" {
		t.Fatalf("user content=%q want hello", got)
	}
}

func TestAnthropicToChatCompletionsBody_PreservesExplicitReasoningEffort(t *testing.T) {
	for _, effort := range []string{"none", "low"} {
		t.Run(effort, func(t *testing.T) {
			req := &apicompat.AnthropicRequest{
				Model:     "kimi-k3",
				MaxTokens: 64,
				Reasoning: &apicompat.ResponsesReasoning{Effort: effort},
				Messages: []apicompat.AnthropicMessage{{
					Role: "user", Content: json.RawMessage(`"hello"`),
				}},
			}
			out, err := anthropicToChatCompletionsBody(req, "kimi-k3")
			requireNoError(t, err)
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != effort {
				t.Fatalf("reasoning_effort=%q want %q; body=%s", got, effort, out)
			}
		})
	}
}

func TestAnthropicToChatCompletionsBody_DoesNotAddDefaultReasoningEffort(t *testing.T) {
	req := &apicompat.AnthropicRequest{
		Model: "kimi-k3",
		Messages: []apicompat.AnthropicMessage{{
			Role: "user", Content: json.RawMessage(`"hello"`),
		}},
	}
	out, err := anthropicToChatCompletionsBody(req, "kimi-k3")
	requireNoError(t, err)
	if got := gjson.GetBytes(out, "reasoning_effort"); got.Exists() {
		t.Fatalf("omitted effort must stay omitted, got %s", got.Raw)
	}
}

func TestTkBridgeCanonicalJSON_InvalidPassthrough(t *testing.T) {
	raw := json.RawMessage(`{not-json`)
	got := tkBridgeCanonicalJSON(raw)
	if string(got) != string(raw) {
		t.Fatalf("invalid JSON must pass through unchanged, got %s", got)
	}
}

func assertJSONEq(t *testing.T, want, got string) {
	t.Helper()
	var wantV, gotV any
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("want unmarshal: %v", err)
	}
	if err := json.Unmarshal([]byte(got), &gotV); err != nil {
		t.Fatalf("got unmarshal: %v (%s)", err, got)
	}
	wantB, _ := json.Marshal(wantV)
	gotB, _ := json.Marshal(gotV)
	if string(wantB) != string(gotB) {
		t.Fatalf("JSON mismatch\nwant: %s\ngot:  %s", wantB, gotB)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
