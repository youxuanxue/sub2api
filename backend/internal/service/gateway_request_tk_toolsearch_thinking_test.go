//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkBodyHasToolSearchTools(t *testing.T) {
	require.True(t, tkBodyHasToolSearchTools([]byte(`{"tools":[{"type":"tool_search_tool_regex_20251119","name":"search"}],"messages":[]}`)))
	require.True(t, tkBodyHasToolSearchTools([]byte(`{"tools":[{"type":"tool_search_tool_bm25_20251119","name":"search"}],"messages":[]}`)))
	require.False(t, tkBodyHasToolSearchTools([]byte(`{"tools":[{"name":"Bash"}],"messages":[]}`)))
	require.False(t, tkBodyHasToolSearchTools([]byte(`{"messages":[]}`)))
}

func TestTkBodyHasSignedThinkingHistory(t *testing.T) {
	history := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"plan","signature":"sig_hist"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
		]
	}`)
	require.True(t, tkBodyHasSignedThinkingHistory(history))

	prefillOnly := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"prefill","signature":"sig_pf"}]}
		]
	}`)
	require.False(t, tkBodyHasSignedThinkingHistory(prefillOnly))
}

func TestTkPrefilterToolSearchHistoricalThinking_StripsHistoricalToolCoupledThinking(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-8",
		"thinking":{"type":"adaptive"},
		"tools":[{"type":"tool_search_tool_regex_20251119","name":"search"},{"name":"Bash"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"task"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"historical reasoning","signature":"EuAGCmMIDh_stale"},
				{"type":"tool_use","id":"toolu_hist","name":"Bash","input":{"command":"ls"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_hist","content":"a.txt"}]},
			{"role":"user","content":[{"type":"text","text":"continue"}]}
		]
	}`)

	out := TkPrefilterToolSearchHistoricalThinking(input, "claude-opus-4-8")
	require.NotEqual(t, string(input), string(out))

	req := parseReq(t, out)
	content := assistantContent(t, req, 1)
	require.Equal(t, 0, countBlockType(content, "thinking"), "historical thinking must be stripped even in tool_use turns under ToolSearch")
	require.Equal(t, 1, countBlockType(content, "tool_use"), "tool_use must remain")
	require.Equal(t, 1, countBlockType(content, "text"), "thinking content should be preserved as text")
}

func TestTkPrefilterToolSearchHistoricalThinking_PreservesAssistantPrefill(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-8",
		"thinking":{"type":"adaptive"},
		"tools":[{"type":"tool_search_tool_regex_20251119","name":"search"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"old","signature":"sig_old"},
				{"type":"text","text":"prior answer"}
			]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"prefill","signature":"sig_pf"}]}
		]
	}`)

	out := TkPrefilterToolSearchHistoricalThinking(input, "claude-opus-4-8")
	req := parseReq(t, out)

	earlier := assistantContent(t, req, 1)
	require.Equal(t, 0, countBlockType(earlier, "thinking"))

	prefill := assistantContent(t, req, 2)
	require.Equal(t, 1, countBlockType(prefill, "thinking"), "assistant prefill thinking must be preserved")
	_, hasThinking := req["thinking"]
	require.True(t, hasThinking, "top-level thinking stays when prefill thinking survives")
}

func TestTkPrefilterToolSearchHistoricalThinking_NoOpWithoutToolSearch(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-8",
		"tools":[{"name":"Bash"}],
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"keep","signature":"sig"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]}
		]
	}`)
	out := TkPrefilterToolSearchHistoricalThinking(input, "claude-opus-4-8")
	require.Equal(t, input, out)
}

func TestTkPrefilterToolSearchHistoricalThinking_SkipsPassbackRequiredUpstream(t *testing.T) {
	input := []byte(`{
		"model":"deepseek-v4-pro",
		"tools":[{"type":"tool_search_tool_regex_20251119","name":"search"}],
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"keep","signature":"sig"}
			]}
		]
	}`)
	out := TkPrefilterToolSearchHistoricalThinking(input, "deepseek-v4-pro")
	require.Equal(t, input, out)
}
