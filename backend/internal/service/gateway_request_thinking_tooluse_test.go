//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the Anthropic tool-use contract: during a tool-use loop the
// thinking blocks of an assistant message that contains tool_use MUST be passed
// back unchanged (signature included). Stripping them orphans the tool_use and
// makes interleaved-thinking clients (Claude Code) report malformed tool calls.
// See FilterThinkingBlocksForRetry.

// Helpers ------------------------------------------------------------------

func parseReq(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var req map[string]any
	require.NoError(t, json.Unmarshal(b, &req))
	return req
}

func assistantContent(t *testing.T, req map[string]any, idx int) []any {
	t.Helper()
	msgs, ok := req["messages"].([]any)
	require.True(t, ok)
	require.Greater(t, len(msgs), idx)
	m, ok := msgs[idx].(map[string]any)
	require.True(t, ok)
	c, ok := m["content"].([]any)
	require.True(t, ok)
	return c
}

func countBlockType(content []any, typ string) int {
	n := 0
	for _, b := range content {
		if bm, ok := b.(map[string]any); ok {
			if t, _ := bm["type"].(string); t == typ {
				n++
			}
		}
	}
	return n
}

// Tests --------------------------------------------------------------------

// The live-captured bug: an assistant turn with signed thinking + tool_use must
// NOT be stripped, and top-level thinking must stay enabled.
func TestFilterThinkingBlocksForRetry_PreservesThinkingInToolUseTurn(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-1",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"List files then read one"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"I should call the tool","signature":"EuAGCmMIDh_realsig"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)
	req := parseReq(t, out)

	// top-level thinking must remain enabled (a preserved thinking block requires it).
	_, hasThinking := req["thinking"]
	require.True(t, hasThinking, "top-level thinking must be kept when tool-coupled thinking is preserved")

	content := assistantContent(t, req, 1)
	require.Equal(t, 1, countBlockType(content, "thinking"), "thinking block must be preserved, not converted to text")
	require.Equal(t, 1, countBlockType(content, "tool_use"), "tool_use must remain")

	// signature must pass through verbatim.
	thinkingBlock := content[0].(map[string]any)
	require.Equal(t, "thinking", thinkingBlock["type"])
	require.Equal(t, "EuAGCmMIDh_realsig", thinkingBlock["signature"])
}

// redacted_thinking in a tool-use turn must also be preserved.
func TestFilterThinkingBlocksForRetry_PreservesRedactedThinkingInToolUseTurn(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-1",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"redacted_thinking","data":"encrypted-blob"},
				{"type":"tool_use","id":"toolu_2","name":"Read","input":{"path":"a.txt"}}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)
	req := parseReq(t, out)

	_, hasThinking := req["thinking"]
	require.True(t, hasThinking)
	content := assistantContent(t, req, 0)
	require.Equal(t, 1, countBlockType(content, "redacted_thinking"), "redacted_thinking must be preserved in a tool-use turn")
	require.Equal(t, 1, countBlockType(content, "tool_use"))
}

// Pure-reasoning assistant turn (NO tool_use) keeps the existing behavior:
// thinking is downgraded to text and top-level thinking is disabled.
func TestFilterThinkingBlocksForRetry_StripsThinkingWhenNoToolUse(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-1",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"pure reasoning","signature":"sig"},
				{"type":"text","text":"Answer"}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)
	req := parseReq(t, out)

	_, hasThinking := req["thinking"]
	require.False(t, hasThinking, "no tool_use → thinking is omittable → top-level thinking disabled")
	content := assistantContent(t, req, 0)
	require.Equal(t, 0, countBlockType(content, "thinking"), "thinking with no tool_use should be downgraded to text")
	require.Equal(t, "text", content[0].(map[string]any)["type"])
	require.Equal(t, "pure reasoning", content[0].(map[string]any)["text"])
}

// Mixed conversation: an EARLIER pure-reasoning turn is stripped while a LATER
// tool-use turn is preserved; because a tool-coupled thinking block survives,
// top-level thinking must stay enabled.
func TestFilterThinkingBlocksForRetry_MixedConversationPreservesOnlyToolTurns(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-1",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q1"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"earlier reasoning","signature":"sig0"},
				{"type":"text","text":"intermediate answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"q2, use a tool"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"tool reasoning","signature":"sigTOOL"},
				{"type":"tool_use","id":"toolu_3","name":"Grep","input":{"q":"x"}}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)
	req := parseReq(t, out)

	// top-level thinking kept (a tool-coupled thinking block survives).
	_, hasThinking := req["thinking"]
	require.True(t, hasThinking)

	// earlier turn (idx 1): thinking stripped to text.
	earlier := assistantContent(t, req, 1)
	require.Equal(t, 0, countBlockType(earlier, "thinking"), "earlier non-tool thinking should be stripped")

	// later tool turn (idx 3): thinking + signature preserved.
	later := assistantContent(t, req, 3)
	require.Equal(t, 1, countBlockType(later, "thinking"), "tool-coupled thinking must be preserved")
	require.Equal(t, "sigTOOL", later[0].(map[string]any)["signature"])
	require.Equal(t, 1, countBlockType(later, "tool_use"))
}

// Contract consistency: when a tool-coupled thinking block is preserved we keep
// thinking enabled, so the clear_thinking_20251015 context-management strategy
// must NOT be removed (it is only invalid when thinking is disabled).
func TestFilterThinkingBlocksForRetry_KeepsClearThinkingStrategyWhenThinkingPreserved(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-1",
		"thinking":{"type":"enabled","budget_tokens":1024},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
		"messages":[
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"t","signature":"sig"},
				{"type":"tool_use","id":"toolu_4","name":"Bash","input":{}}
			]}
		]
	}`)

	out := FilterThinkingBlocksForRetry(input)
	req := parseReq(t, out)

	cm, ok := req["context_management"].(map[string]any)
	require.True(t, ok, "context_management must be kept when thinking stays enabled")
	edits, ok := cm["edits"].([]any)
	require.True(t, ok)
	require.Len(t, edits, 1)
	require.Equal(t, "clear_thinking_20251015", edits[0].(map[string]any)["type"])
}
