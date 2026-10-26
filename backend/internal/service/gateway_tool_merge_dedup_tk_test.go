//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The additional_tools carrier lift used to bare-append, so a client declaring
// a tool both at top level and in the carrier sent two identical entries
// upstream (observed as two `shell`, and as two `web_search` once the Anthropic
// exit maps the native form onto a same-named function).
func TestLiftResponsesAdditionalTools_DropsDuplicateDeclaration(t *testing.T) {
	body := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"tools":[{"type":"function","name":"shell","description":"client shell"}],
		"input":[{"type":"additional_tools","tools":[{"type":"function","name":"shell","description":"carrier shell"}]}]
	}`), &body))

	changed, err := liftResponsesAdditionalTools(body)
	require.NoError(t, err)
	assert.True(t, changed, "the carrier item is still removed from input")

	tools, ok := body["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1, "a repeated declaration must not be forwarded twice")
	// First writer wins: the client's top-level declaration survives intact.
	assert.Equal(t, "client shell", tools[0].(map[string]any)["description"])

	input, ok := body["input"].([]any)
	require.True(t, ok)
	assert.Empty(t, input, "the carrier item must be consumed")
}

func TestLiftResponsesAdditionalTools_DropsDuplicateServerTool(t *testing.T) {
	body := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"tools":[{"type":"web_search"}],
		"input":[{"type":"additional_tools","tools":[{"type":"web_search"}]}]
	}`), &body))

	_, err := liftResponsesAdditionalTools(body)
	require.NoError(t, err)
	require.Len(t, body["tools"].([]any), 1)
}

// Benign direction: distinct carrier tools are all promoted, in carrier order,
// and a request with no carrier is untouched.
func TestLiftResponsesAdditionalTools_KeepsDistinctAndIgnoresAbsentCarrier(t *testing.T) {
	body := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(`{
		"tools":[{"type":"function","name":"shell"}],
		"input":[{"type":"additional_tools","tools":[{"type":"function","name":"apply_patch"},{"type":"web_search"}]}]
	}`), &body))

	changed, err := liftResponsesAdditionalTools(body)
	require.NoError(t, err)
	assert.True(t, changed)
	tools := body["tools"].([]any)
	require.Len(t, tools, 3)
	assert.Equal(t, "shell", tools[0].(map[string]any)["name"])
	assert.Equal(t, "apply_patch", tools[1].(map[string]any)["name"])
	assert.Equal(t, "web_search", tools[2].(map[string]any)["type"])

	noCarrier := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"function","name":"shell"}],"input":[{"type":"message","role":"user"}]}`), &noCarrier))
	changed, err = liftResponsesAdditionalTools(noCarrier)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Len(t, noCarrier["tools"].([]any), 1)
}

// The Moonshot K3 promotion lifts tools out of a dynamic system message. A
// client that also declared the tool at top level used to get two copies.
func TestApplyMoonshotK3DynamicToolsShape_DropsDuplicateDeclaration(t *testing.T) {
	model := moonshotK3DynamicToolsTestModel(t)
	body := []byte(`{"model":"` + model + `","tools":[{"type":"function","function":{"name":"calc","description":"client calc"}}],` +
		`"messages":[{"role":"system","tools":[{"type":"function","function":{"name":"calc","description":"dynamic calc"}}]},{"role":"user","content":"hi"}]}`)

	out := applyMoonshotK3DynamicToolsShape(model, body)

	tools := gjson.GetBytes(out, "tools").Array()
	require.Len(t, tools, 1, "the same tool declared twice must be promoted once")
	assert.Equal(t, "client calc", tools[0].Get("function.description").String())
	// The dynamic system message is still consumed.
	messages := gjson.GetBytes(out, "messages").Array()
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].Get("role").String())
}

// Benign direction: a distinct dynamic tool is still promoted alongside the
// client's own declaration.
func TestApplyMoonshotK3DynamicToolsShape_PromotesDistinctTool(t *testing.T) {
	model := moonshotK3DynamicToolsTestModel(t)
	body := []byte(`{"model":"` + model + `","tools":[{"type":"function","function":{"name":"calc"}}],` +
		`"messages":[{"role":"system","tools":[{"type":"function","function":{"name":"search"}}]},{"role":"user","content":"hi"}]}`)

	out := applyMoonshotK3DynamicToolsShape(model, body)

	tools := gjson.GetBytes(out, "tools").Array()
	require.Len(t, tools, 2)
	assert.Equal(t, "calc", tools[0].Get("function.name").String())
	assert.Equal(t, "search", tools[1].Get("function.name").String())
}

// moonshotK3DynamicToolsTestModel returns a model that actually passes the
// promotion's own gate, so the test cannot pass by never running the code.
func moonshotK3DynamicToolsTestModel(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"kimi-k3", "kimi-k3-0711-preview", "moonshot-v1-8k", "kimi-k2-0905-preview"} {
		if isNewAPIAliFixedSamplingModel(candidate) {
			return candidate
		}
	}
	t.Skip("no model satisfies isNewAPIAliFixedSamplingModel; promotion gate changed")
	return ""
}
