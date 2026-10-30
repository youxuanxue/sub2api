package antigravity

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanJSONSchemaRejectsUnsafeExpansion(t *testing.T) {
	var self, mutual map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"$ref":"#/$defs/Node","$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}}}`), &self))
	require.NoError(t, json.Unmarshal([]byte(`{"$ref":"#/$defs/A","$defs":{"A":{"$ref":"#/$defs/B"},"B":{"$ref":"#/$defs/A"}}}`), &mutual))
	deep := map[string]any{"type": "string"}
	for i := 0; i < maxSchemaExpansionDepth+1; i++ {
		deep = map[string]any{"type": "array", "items": deep}
	}
	// An acyclic DAG is small on the wire but doubles at each inline level.
	defs := map[string]any{"N0": map[string]any{"type": "string"}}
	for i := 1; i <= 16; i++ {
		ref := map[string]any{"$ref": fmt.Sprintf("#/$defs/N%d", i-1)}
		defs[fmt.Sprintf("N%d", i)] = map[string]any{"type": "object", "properties": map[string]any{"left": ref, "right": ref}}
	}
	exponential := map[string]any{"$ref": "#/$defs/N16", "$defs": defs}
	wide := map[string]any{"type": "string", "enum": make([]any, maxSchemaExpansionNodes+1)}
	largeDefs := map[string]any{"A": map[string]any{"type": "string", "description": strings.Repeat("x", 1<<20)}}
	largeProps := map[string]any{}
	for i := 0; i < 32; i++ {
		largeProps[fmt.Sprintf("value%d", i)] = map[string]any{"$ref": "#/$defs/A"}
	}
	repeatedBytes := map[string]any{"type": "object", "properties": largeProps, "$defs": largeDefs}
	for name, schema := range map[string]map[string]any{"expanded bytes": repeatedBytes, "self": self, "mutual": mutual, "depth": deep, "acyclic expansion": exponential, "wide": wide} {
		t.Run(name, func(t *testing.T) {
			before, err := json.Marshal(schema)
			require.NoError(t, err)
			cleaned, err := CleanJSONSchema(schema)
			var unsafe *SchemaExpansionError
			require.ErrorAs(t, err, &unsafe)
			require.Nil(t, cleaned, "must not return a weakened schema on failure")
			after, err := json.Marshal(schema)
			require.NoError(t, err)
			require.JSONEq(t, string(before), string(after), "rejection must not mutate the request")
		})
	}
}

func TestCleanJSONSchemaSharedReferencesRemainIndependent(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"type":"object","properties":{"first":{"$ref":"#/$defs/Value"},"second":{"$ref":"#/$defs/Value","description":"override"}},"$defs":{"Value":{"type":"string","description":"default"}}}`), &schema))
	cleaned, err := CleanJSONSchema(schema)
	require.NoError(t, err)
	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	first, ok := props["first"].(map[string]any)
	require.True(t, ok)
	second, ok := props["second"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", first["description"])
	require.Equal(t, "override", second["description"])
	first["description"] = "changed"
	require.Equal(t, "override", second["description"])
	require.Contains(t, schema, "$defs", "cleaning should not consume input definitions")
}

func TestTransformClaudeToGeminiRejectsUnsafeSchema(t *testing.T) {
	var schema map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{"$ref":"#/$defs/Node","$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}}}`), &schema))
	req := &ClaudeRequest{Model: "claude-sonnet-4-6", Tools: []ClaudeTool{{Name: "walk", InputSchema: schema}}}
	body, err := TransformClaudeToGemini(req, "project", "claude-sonnet-4-6")
	var unsafe *SchemaExpansionError
	require.ErrorAs(t, err, &unsafe)
	require.Nil(t, body)
}

func TestCleanJSONSchemaReferenceSiblingsAndAliases(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"sibling reuse", `{"$ref":"#/$defs/A","properties":{"child":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"object","properties":{"leaf":{"type":"string"}}}}}`, `{"type":"object","properties":{"child":{"type":"object","properties":{"leaf":{"type":"string"}}}}}`},
		{"alias chain", `{"$ref":"#/$defs/A","$defs":{"A":{"$ref":"#/$defs/B"},"B":{"type":"string"}}}`, `{"type":"string"}`},
		{"overridden recursive branch", `{"$ref":"#/$defs/A","properties":{"leaf":{"type":"string"}},"$defs":{"A":{"$ref":"#/$defs/B"},"B":{"type":"object","properties":{"next":{"$ref":"#/$defs/A"}}}}}`, `{"type":"object","properties":{"leaf":{"type":"string"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.input), &input))
			got, err := CleanJSONSchema(input)
			require.NoError(t, err)
			raw, err := json.Marshal(got)
			require.NoError(t, err)
			require.JSONEq(t, tc.want, string(raw))
		})
	}
}
