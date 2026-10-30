//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanGeminiRequest_ResponseSchemaAliases(t *testing.T) {
	for _, genKey := range []string{"generationConfig", "generation_config"} {
		for _, key := range []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"} {
			t.Run(genKey+"/"+key, func(t *testing.T) {
				body := []byte(`{"` + genKey + `":{"responseMimeType":"application/json","` + key + `":{"type":"object","properties":{"answer":{"$ref":"#/$defs/Answer"}},"required":["answer"],"$defs":{"Answer":{"type":"string"}}}}}`)
				out, err := cleanGeminiRequest(body)
				require.NoError(t, err)
				var payload map[string]any
				require.NoError(t, json.Unmarshal(out, &payload))
				gen := payload[genKey].(map[string]any)
				require.Equal(t, "application/json", gen["responseMimeType"])
				require.Equal(t, map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []any{"answer"}}, gen["responseSchema"])
				for _, alias := range []string{"responseJsonSchema", "response_schema", "response_json_schema"} {
					require.NotContains(t, gen, alias)
				}
			})
		}
	}
}

func TestCleanGeminiRequest_ResponseSchemaDoesNotInventToolArguments(t *testing.T) {
	out, err := cleanGeminiRequest([]byte(`{"generationConfig":{"responseJsonSchema":{"type":"object"}},"tools":[{"functionDeclarations":[{"name":"ping","parameters":{"type":"object"}}]}]}`))
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(out, &payload))
	schema := payload["generationConfig"].(map[string]any)["responseSchema"]
	require.Equal(t, map[string]any{"type": "object"}, schema)
	tools := payload["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	require.Contains(t, tools["parameters"].(map[string]any)["properties"], "reason")
}

func TestCleanGeminiRequest_ResponseSchemaNestedEmptyObjects(t *testing.T) {
	out, err := cleanGeminiRequest([]byte(`{"generationConfig":{"responseJsonSchema":{"type":"object","properties":{"record":{"type":"object"},"list":{"type":"array","items":{"type":"object"}}}}}}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"generationConfig":{"responseSchema":{"type":"object","properties":{"record":{"type":"object"},"list":{"type":"array","items":{"type":"object"}}}}}}`, string(out))
}

func TestCleanGeminiRequest_ResponseSchemaCanonicalWins(t *testing.T) {
	for _, canonical := range []string{`{"type":"string"}`, `null`, `false`} {
		out, err := cleanGeminiRequest([]byte(`{"generationConfig":{"responseSchema":` + canonical + `,"responseJsonSchema":{"type":"integer"}}}`))
		require.NoError(t, err)
		require.JSONEq(t, `{"generationConfig":{"responseSchema":`+canonical+`}}`, string(out))
	}
}

func TestAntigravityGeminiWire_NeutralizesSDKIdentity(t *testing.T) {
	const opener = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	for _, key := range []string{"systemInstruction", "system_instruction"} {
		t.Run(key, func(t *testing.T) {
			payload := map[string]any{key: map[string]any{"parts": []any{map[string]any{"text": "You are Antigravity"}, map[string]any{"text": "x-anthropic-billing-header: metadata\n" + opener + "\nKeep project rules."}, map[string]any{"text": "Example: " + opener}}}, "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": opener}}}}}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			out, err := (&AntigravityGatewayService{}).normalizeForwardGeminiGenerateContentBody(body)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			require.NotContains(t, got, "system_instruction")
			parts := got["systemInstruction"].(map[string]any)["parts"].([]any)
			require.Len(t, parts, 3)
			require.Equal(t, "You are an AI agent.\nKeep project rules.", parts[1].(map[string]any)["text"])
			require.Equal(t, "Example: "+opener, parts[2].(map[string]any)["text"])
			require.Equal(t, payload["contents"], got["contents"])
			again, err := injectIdentityPatchToGeminiRequest(out)
			require.NoError(t, err)
			require.JSONEq(t, string(out), string(again))
		})
	}
	body, err := convertClaudeMessagesToGeminiGenerateContent([]byte(`{"system":"` + opener + `\nKeep project rules.","messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	out, err := (&AntigravityGatewayService{}).normalizeForwardGeminiGenerateContentBody(body)
	require.NoError(t, err)
	require.NotContains(t, string(out), "Claude Agent SDK")
	require.Contains(t, string(out), "Keep project rules.")
}
