//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// A client that implements web_search itself declares argument schema. Replacing
// that declaration with {"type":"web_search"} routed the call to xAI's native
// search: the model never emitted the client's tool_call, so the client waited
// for a result that could not arrive. Its declaration must survive intact.
func TestAppendGrokFreeCacheNativeTools_KeepsClientImplementedSearchFunction(t *testing.T) {
	body := []byte(`{"tools":[` +
		`{"type":"function","name":"web_search","description":"MY own search impl","parameters":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}},` +
		`{"type":"function","name":"view_image","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)

	out, err := appendGrokFreeCacheNativeToolsWithPolicy(body, true, true)
	require.NoError(t, err)

	tools := gjson.GetBytes(out, "tools").Array()
	var clientSearch gjson.Result
	nativeWebSearch := false
	for _, tool := range tools {
		switch {
		case tool.Get("type").String() == "web_search":
			nativeWebSearch = true
		case tool.Get("type").String() == "function" && tool.Get("name").String() == "web_search":
			clientSearch = tool
		}
	}

	assert.False(t, nativeWebSearch, "a client-implemented web_search must not be replaced by the native tool")
	require.True(t, clientSearch.Exists(), "the client's web_search function must reach the upstream")
	assert.Equal(t, "MY own search impl", clientSearch.Get("description").String())
	assert.Equal(t, "string", clientSearch.Get("parameters.properties.q.type").String())
	assert.Equal(t, []any{"q"}, clientSearch.Get("parameters.required").Value())
}

// Benign direction: a capability-only declaration (Grok Build style, no
// argument schema) still converts, keeping Free OAuth cache-capable.
func TestAppendGrokFreeCacheNativeTools_ConvertsBareSearchDeclaration(t *testing.T) {
	for name, tool := range map[string]string{
		"no parameters":        `{"type":"function","name":"web_search","description":"search"}`,
		"empty object schema":  `{"type":"function","name":"web_search","description":"search","parameters":{"type":"object"}}`,
		"empty properties map": `{"type":"function","name":"web_search","parameters":{"type":"object","properties":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"tools":[` + tool + `,{"type":"function","name":"view_image"}]}`)
			out, err := appendGrokFreeCacheNativeToolsWithPolicy(body, true, true)
			require.NoError(t, err)

			converted := false
			for _, got := range gjson.GetBytes(out, "tools").Array() {
				if got.Get("type").String() == "web_search" {
					converted = true
				}
			}
			assert.True(t, converted, "a bare search declaration should become the native tool")
		})
	}
}

// allowFunctionSearch=false never converted and must stay that way.
func TestAppendGrokFreeCacheNativeTools_FunctionSearchDisabledKeepsClientForm(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"web_search","description":"search"}]}`)
	out, err := appendGrokFreeCacheNativeToolsWithPolicy(body, true, false)
	require.NoError(t, err)

	tools := gjson.GetBytes(out, "tools").Array()
	assert.Equal(t, "function", tools[0].Get("type").String())
	assert.Equal(t, "web_search", tools[0].Get("name").String())
}

func TestGrokClientOwnsSearchFunction_SignalIsDeclaredArgumentSchema(t *testing.T) {
	owns := func(raw string) bool {
		return grokClientOwnsSearchFunction(gjson.Parse(raw))
	}

	assert.True(t, owns(`{"type":"function","name":"web_search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}`))
	// Description alone is not the signal: Grok Build's own declaration has one.
	assert.False(t, owns(`{"type":"function","name":"web_search","description":"search"}`))
	assert.False(t, owns(`{"type":"function","name":"web_search","parameters":{"type":"object"}}`))
	assert.False(t, owns(`{"type":"function","name":"web_search"}`))
}
