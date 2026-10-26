package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A carrier tool repeating a top-level declaration used to reach the upstream
// twice, because every merge point appended without dedupe.
func TestEffectiveResponsesTools_DropsCarrierDuplicate(t *testing.T) {
	req := &ResponsesRequest{
		Tools: []ResponsesTool{{Type: "function", Name: "shell", Description: "client shell"}},
		Input: json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"function","name":"shell","description":"carrier shell"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1, "a repeated declaration must not be forwarded twice")
	// First writer wins: the client's own top-level declaration survives.
	assert.Equal(t, "client shell", tools[0].Description)
}

// Server tools are keyed by type when they carry no name.
func TestEffectiveResponsesTools_DropsDuplicateServerTool(t *testing.T) {
	req := &ResponsesRequest{
		Tools: []ResponsesTool{{Type: "web_search"}},
		Input: json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"web_search"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "web_search", tools[0].Type)
}

// Benign direction: distinct tools all survive, in order.
func TestEffectiveResponsesTools_KeepsDistinctTools(t *testing.T) {
	req := &ResponsesRequest{
		Tools: []ResponsesTool{{Type: "function", Name: "shell"}},
		Input: json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"function","name":"apply_patch"},{"type":"web_search"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 3)
	assert.Equal(t, "shell", tools[0].Name)
	assert.Equal(t, "apply_patch", tools[1].Name)
	assert.Equal(t, "web_search", tools[2].Type)
}

// A function and a server tool of the same name are different declarations and
// must both survive; only exact type+name repeats collapse.
func TestEffectiveResponsesTools_KeepsSameNameAcrossTypes(t *testing.T) {
	req := &ResponsesRequest{
		Tools: []ResponsesTool{{Type: "function", Name: "web_search", Description: "mine"}},
		Input: json.RawMessage(`[{"type":"additional_tools","tools":[{"type":"web_search"}]}]`),
	}

	tools, err := EffectiveResponsesTools(req)
	require.NoError(t, err)
	require.Len(t, tools, 2)
	assert.Equal(t, "function", tools[0].Type)
	assert.Equal(t, "web_search", tools[1].Type)
}

func TestResponsesToolDedupKey_DistinguishesTypeAndName(t *testing.T) {
	fn := ResponsesTool{Type: "function", Name: "web_search"}
	server := ResponsesTool{Type: "web_search"}
	assert.NotEqual(t, ResponsesToolDedupKey(fn), ResponsesToolDedupKey(server))
	assert.Equal(t, ResponsesToolDedupKey(fn), ResponsesToolDedupKey(ResponsesTool{Type: "function", Name: "web_search", Description: "ignored"}),
		"identity is type+name; a differing description is still the same tool")
}

func TestAppendResponsesToolsDeduped_ReportsWhetherAnythingWasAdded(t *testing.T) {
	base := []ResponsesTool{{Type: "function", Name: "shell"}}

	same, appended := AppendResponsesToolsDeduped(base, ResponsesTool{Type: "function", Name: "shell"})
	assert.False(t, appended)
	assert.Len(t, same, 1)

	grown, appended := AppendResponsesToolsDeduped(base, ResponsesTool{Type: "function", Name: "edit"})
	assert.True(t, appended)
	assert.Len(t, grown, 2)

	unchanged, appended := AppendResponsesToolsDeduped(base)
	assert.False(t, appended)
	assert.Len(t, unchanged, 1)
}

// The raw-map variant must also see the Chat Completions nesting,
// {"function":{"name":...}}, used by the Moonshot K3 promotion.
func TestRawResponsesToolDedupKey_ReadsNestedFunctionName(t *testing.T) {
	nested := map[string]any{"type": "function", "function": map[string]any{"name": "calc"}}
	flat := map[string]any{"type": "function", "name": "calc"}
	assert.Equal(t, RawResponsesToolDedupKey(flat), RawResponsesToolDedupKey(nested))
}

func TestAppendRawResponsesToolsDeduped_CollapsesRepeatsKeepsDistinct(t *testing.T) {
	base := []any{map[string]any{"type": "function", "function": map[string]any{"name": "calc"}}}

	same, appended := AppendRawResponsesToolsDeduped(base,
		map[string]any{"type": "function", "function": map[string]any{"name": "calc"}})
	assert.False(t, appended)
	assert.Len(t, same, 1)

	grown, appended := AppendRawResponsesToolsDeduped(base,
		map[string]any{"type": "function", "function": map[string]any{"name": "sum"}})
	assert.True(t, appended)
	assert.Len(t, grown, 2)
}

// Non-object entries keep their own identity rather than collapsing together.
func TestAppendRawResponsesToolsDeduped_KeepsDistinctNonObjects(t *testing.T) {
	merged, appended := AppendRawResponsesToolsDeduped([]any{"web_search"}, "x_search")
	assert.True(t, appended)
	assert.Len(t, merged, 2)
}
