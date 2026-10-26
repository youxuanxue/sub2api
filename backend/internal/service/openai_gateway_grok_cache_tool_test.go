package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAppendMissingGrokFreeCacheNativeTools_PureClientFunctionNoInject(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View image","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		],
		"tool_choice": "auto"
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	for _, tool := range tools {
		toolType := tool.Get("type").String()
		assert.NotEqual(t, "web_search", toolType, "should not inject web_search for pure client functions")
		assert.NotEqual(t, "x_search", toolType, "should not inject x_search for pure client functions")
	}
}

func TestAppendMissingGrokFreeCacheNativeTools_FunctionPlusWebSearchInjects(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"web_search","description":"Search","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	// This client declared web_search with its own argument schema, so it
	// implements the tool and must receive the tool_call. The declaration stays
	// a function instead of being converted to xAI's native search; x_search is
	// still injected to keep the request on the cache-capable tier.
	assert.False(t, types["web_search"], "a client-implemented web_search must not be converted to the native tool")
	assert.True(t, types["x_search"], "x_search should be injected when a search tool is present alongside client functions")
	var clientSearch gjson.Result
	for _, tool := range tools {
		if tool.Get("type").String() == "function" && tool.Get("name").String() == "web_search" {
			clientSearch = tool
		}
	}
	require.True(t, clientSearch.Exists(), "client web_search function must survive")
	assert.Equal(t, "Search", clientSearch.Get("description").String())
	assert.Equal(t, "string", clientSearch.Get("parameters.properties.query.type").String())
}

// Capability-only search declarations (Grok Build style: no argument schema)
// still convert, so Free OAuth stays cache-capable.
func TestAppendMissingGrokFreeCacheNativeTools_BareSearchFunctionStillConverts(t *testing.T) {
	for name, params := range map[string]string{
		"absent parameters": "",
		"empty object":      `,"parameters":{"type":"object"}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"grok-4.5","tools":[{"type":"function","name":"view_image","parameters":{"type":"object","properties":{"path":{"type":"string"}}}},{"type":"function","name":"web_search","description":"search"` + params + `}]}`)
			result, err := appendMissingGrokFreeCacheNativeTools(body)
			require.NoError(t, err)
			types := make(map[string]bool)
			for _, tool := range gjson.GetBytes(result, "tools").Array() {
				types[tool.Get("type").String()] = true
			}
			assert.True(t, types["web_search"], "bare search declaration should convert to the native tool")
			assert.True(t, types["x_search"], "x_search should be injected alongside")
		})
	}
}

func TestAppendMissingGrokFreeCacheNativeTools_NativeSearchAlreadyPresent(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"web_search"}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	types := make(map[string]bool)
	for _, tool := range tools {
		types[tool.Get("type").String()] = true
	}
	assert.True(t, types["web_search"])
	assert.True(t, types["x_search"], "x_search should be injected when web_search is already present")
}

func TestAppendMissingGrokFreeCacheNativeTools_MultipleFunctionsNoSearch(t *testing.T) {
	body := []byte(`{
		"model": "grok-4.5",
		"tools": [
			{"type":"function","name":"view_image","description":"View","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}},
			{"type":"function","name":"read_file","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}
		]
	}`)

	result, err := appendMissingGrokFreeCacheNativeTools(body)
	require.NoError(t, err)

	tools := gjson.GetBytes(result, "tools").Array()
	require.Len(t, tools, 2, "no tools should be injected for pure client functions")
}
