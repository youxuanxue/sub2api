package antigravity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Schema shapes that the cleaner used to flatten away. The existing
// schema_cleaner_test.go cases are all prefixItems-shaped and at most two
// levels deep, which left these forms unguarded.

// A type:"array" node with an anyOf sibling used to skip union merging (the
// gate admitted only "" and "object"), so the whitelist filter deleted anyOf
// along with the items inside its branches, and the array fallback then refilled
// items as {type:string}: array-of-object silently became array-of-string.
// nestedField reads one object-valued member, failing the test when the shape
// is not what the assertion assumes.
func nestedField(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	child, ok := parent[key].(map[string]any)
	require.True(t, ok, "%s must be an object", key)
	return child
}

func TestCleanJSONSchema_ArrayWithAnyOfKeepsItemStructure(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"records": map[string]any{
				"type": "array",
				"anyOf": []any{
					map[string]any{"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":   map[string]any{"type": "integer"},
							"name": map[string]any{"type": "string"},
						},
						"required": []any{"id"},
					}},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	cleanedProps, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	records, ok := cleanedProps["records"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "array", records["type"])
	assert.Nil(t, records["anyOf"], "merged union keyword must be removed")

	items, ok := records["items"].(map[string]any)
	require.True(t, ok, "records.items must be an object")
	assert.Equal(t, "object", items["type"], "array-of-object must not degrade to array-of-string")
	props, ok := items["properties"].(map[string]any)
	require.True(t, ok, "item properties must survive the union merge")
	assert.Equal(t, "integer", nestedField(t, props, "id")["type"])
	assert.Equal(t, "string", nestedField(t, props, "name")["type"])
	assert.Equal(t, []any{"id"}, items["required"])
}

// Draft-07 tuple validation (items as an array) used to keep only the single
// best-scoring entry, discarding every other position's declaration. Gemini
// accepts one items schema, so positions cannot be preserved, but the declared
// fields must be.
func TestCleanJSONSchema_TupleItemsMergeInsteadOfDiscard(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entry": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"key": map[string]any{"type": "string"}},
						"required":   []any{"key"},
					},
					map[string]any{
						"type":       "object",
						"properties": map[string]any{"weight": map[string]any{"type": "number"}},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	require.NotNil(t, cleaned)

	entry := nestedField(t, nestedField(t, cleaned, "properties"), "entry")
	items, ok := entry["items"].(map[string]any)
	require.True(t, ok, "tuple items must collapse to a single schema object")
	props, ok := items["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "key", "first tuple position must survive")
	assert.Contains(t, props, "weight", "second tuple position must not be discarded")
	// A per-position required field does not hold for the other positions.
	assert.Nil(t, items["required"], "tuple required must not be promoted to every element")
}

func TestCleanJSONSchema_TupleItemsMergesEnumValues(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op": map[string]any{
				"type": "array",
				"items": []any{
					map[string]any{"enum": []any{"read"}},
					map[string]any{"enum": []any{"write"}},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	items := nestedField(t, nestedField(t, nestedField(t, cleaned, "properties"), "op"), "items")
	assert.Equal(t, "string", items["type"])
	assert.ElementsMatch(t, []any{"read", "write"}, items["enum"])
}

// The empty-object fallback ran on every object node, so a string map
// ({type:object, additionalProperties:{type:string}}) inside items was read as
// having no properties and got a required "reason" field the client never
// declared. The nested placeholder must stay optional.
func TestCleanJSONSchema_NestedRecordKeepsReasonOut(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"envs": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": map[string]any{"type": "string"},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	items := nestedField(t, nestedField(t, nestedField(t, cleaned, "properties"), "envs"), "items")
	assert.Equal(t, "object", items["type"])
	props, ok := items["properties"].(map[string]any)
	require.True(t, ok, "Gemini requires object nodes to declare properties")
	assert.NotContains(t, props, "reason", "nested objects must not get the tool-level reason field")
	assert.Nil(t, items["required"], "a placeholder property must never be required")
}

// Benign direction: a tool that genuinely takes no arguments still gets the
// upstream reason placeholder at the parameter root.
func TestCleanJSONSchema_RootEmptyObjectKeepsReasonPlaceholder(t *testing.T) {
	cleaned := CleanJSONSchema(map[string]any{"type": "object"})
	require.NotNil(t, cleaned)
	props, ok := cleaned["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "reason")
	assert.Equal(t, []any{"reason"}, cleaned["required"])
}

// Benign direction: array -> object -> array -> object must pass through with
// every level intact.
func TestCleanJSONSchema_DeepNestingUnchanged(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"groups": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"members": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type":       "object",
								"properties": map[string]any{"email": map[string]any{"type": "string"}},
							},
						},
					},
				},
			},
		},
	}

	cleaned := CleanJSONSchema(input)
	groups := nestedField(t, nestedField(t, cleaned, "properties"), "groups")
	members := nestedField(t, nestedField(t, nestedField(t, groups, "items"), "properties"), "members")
	assert.Equal(t, "array", members["type"])
	member := nestedField(t, members, "items")
	assert.Equal(t, "object", member["type"])
	assert.Equal(t, "string", nestedField(t, nestedField(t, member, "properties"), "email")["type"])
	assert.NotContains(t, member["properties"], "reason")
}
