//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTKCleanToolSchemaExpandsReferencesWithoutMutatingInput(t *testing.T) {
	for _, definitions := range []string{"$defs", "definitions"} {
		t.Run(definitions, func(t *testing.T) {
			var schema map[string]any
			raw := fmt.Sprintf(`{"type":"object",%q:{"Item":{"type":"object","properties":{"id":{"type":"integer"},"label":{"type":"string"}},"required":["id"]}},"properties":{"rows":{"type":"array","items":{"$ref":"#/%s/Item"}},"single":{"$ref":"#/%s/Item","description":"one item"}}}`, definitions, definitions, definitions)
			require.NoError(t, json.Unmarshal([]byte(raw), &schema))
			result, err := tkCleanToolSchema(schema)
			require.NoError(t, err)
			out, err := json.Marshal(result)
			require.NoError(t, err)
			require.Equal(t, "OBJECT", gjson.GetBytes(out, "properties.rows.items.type").String())
			require.Equal(t, "INTEGER", gjson.GetBytes(out, "properties.rows.items.properties.id.type").String())
			require.Equal(t, "STRING", gjson.GetBytes(out, "properties.rows.items.properties.label.type").String())
			require.Equal(t, `["id"]`, gjson.GetBytes(out, "properties.rows.items.required").Raw)
			require.Equal(t, "one item", gjson.GetBytes(out, "properties.single.description").String())
			require.Equal(t, "INTEGER", gjson.GetBytes(out, "properties.single.properties.id.type").String())
			require.NotContains(t, result, definitions)
			after, err := json.Marshal(schema)
			require.NoError(t, err)
			require.JSONEq(t, raw, string(after))
		})
	}
}

func TestGeminiMessagesToolReferencesReachWireSchema(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":16,"messages":[{"role":"user","content":"use edits"}],"tools":[{"name":"edits","input_schema":{"type":"object","$defs":{"Item":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}},"properties":{"rows":{"type":"array","items":{"$ref":"#/$defs/Item"}}}}}]}`)
	out, err := convertClaudeMessagesToGeminiGenerateContent(body)
	require.NoError(t, err)
	require.Equal(t, "OBJECT", gjson.GetBytes(out, "tools.0.functionDeclarations.0.parameters.properties.rows.items.type").String())
	require.Equal(t, "INTEGER", gjson.GetBytes(out, "tools.0.functionDeclarations.0.parameters.properties.rows.items.properties.id.type").String())
	require.Equal(t, `["id"]`, gjson.GetBytes(out, "tools.0.functionDeclarations.0.parameters.properties.rows.items.required").Raw)
}

func TestGeminiMessagesUnsafeToolSchemaRejectedBeforeUpstream(t *testing.T) {
	defs := map[string]any{"N0": map[string]any{"type": "string"}}
	for i := 1; i <= 16; i++ {
		ref := map[string]any{"$ref": fmt.Sprintf("#/$defs/N%d", i-1)}
		defs[fmt.Sprintf("N%d", i)] = map[string]any{"type": "object", "properties": map[string]any{"left": ref, "right": ref}}
	}
	cases := map[string]any{
		"recursive":   map[string]any{"$ref": "#/$defs/Node", "$defs": map[string]any{"Node": map[string]any{"type": "object", "properties": map[string]any{"next": map[string]any{"$ref": "#/$defs/Node"}}}}},
		"exponential": map[string]any{"$ref": "#/$defs/N16", "$defs": defs},
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": "gemini-3.8-flash", "max_tokens": 16, "messages": []any{map[string]any{"role": "user", "content": "hi"}}, "tools": []any{map[string]any{"name": "edit", "input_schema": schema}}})
			require.NoError(t, err)
			converted, err := convertClaudeMessagesToGeminiGenerateContent(body)
			var expansionErr *antigravity.SchemaExpansionError
			require.ErrorAs(t, err, &expansionErr)
			require.Nil(t, converted)
			upstream := &queuedHTTPUpstreamStub{}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
			c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)
			result, err := svc.Forward(context.Background(), c, geminiCompatThinkingTestAccount(), body)
			require.Error(t, err)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "unsupported JSON schema")
			require.Empty(t, upstream.requestBodies)
		})
	}
}
