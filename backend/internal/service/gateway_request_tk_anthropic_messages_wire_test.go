//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func countContentTypes(content gjson.Result) (thinking, toolUse, text int) {
	for _, b := range content.Array() {
		switch b.Get("type").String() {
		case "thinking", "redacted_thinking":
			thinking++
		case "tool_use":
			toolUse++
		case "text":
			text++
		}
	}
	return
}

func TestTkPrepareAnthropicMessagesWireBody_AppliesToolStormPrefilter(t *testing.T) {
	// Post-ToolSearch storm: tools no longer list tool_search, but historical
	// assistant turns still carry signed thinking + tool_use (prod 2026-09-25).
	input := []byte(`{
		"model":"claude-fable-5",
		"thinking":{"type":"adaptive"},
		"tools":[{"name":"Bash"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"task"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"historical","signature":"EuAG_stale"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}
		]
	}`)

	out := tkPrepareAnthropicMessagesWireBody(nil, input, "claude-fable-5")
	require.NotEqual(t, string(input), string(out))

	thinking, toolUse, text := countContentTypes(gjson.GetBytes(out, "messages.1.content"))
	require.Equal(t, 0, thinking)
	require.Equal(t, 1, toolUse)
	require.Equal(t, 1, text)
}

func TestTkRectifyAnthropicThinkingContract400_CannotBeModifiedStripsHistorical(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-8",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"old","signature":"sig_old"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"continue"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"latest","signature":"sig_latest"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]}
		]
	}`)
	respBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0: thinking or redacted_thinking blocks in the latest assistant message cannot be modified."}}`)

	out, kind, ok := tkRectifyAnthropicThinkingContract400(body, "claude-opus-4-8", respBody)
	require.True(t, ok)
	require.Equal(t, "thinking_cannot_modify_strip_historical", kind)

	histThinking, _, _ := countContentTypes(gjson.GetBytes(out, "messages.1.content"))
	require.Equal(t, 0, histThinking, "historical thinking must be stripped")

	latestThinking, latestTool, _ := countContentTypes(gjson.GetBytes(out, "messages.3.content"))
	require.Equal(t, 1, latestThinking, "latest tool-coupled thinking preserved on first rectify stage")
	require.Equal(t, 1, latestTool)
}

func TestTkRectifyAnthropicThinkingContract400_CannotBeModifiedEscalatesWhenNoHistorical(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-8",
		"tools":[{"name":"Bash"}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"latest","signature":"sig"},
				{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}
			]}
		]
	}`)
	respBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking blocks in the latest assistant message cannot be modified"}}`)

	out, kind, ok := tkRectifyAnthropicThinkingContract400(body, "claude-opus-4-8", respBody)
	require.True(t, ok)
	require.Equal(t, "thinking_cannot_modify_signature_sensitive", kind)

	_, toolUse, _ := countContentTypes(gjson.GetBytes(out, "messages.1.content"))
	require.Equal(t, 0, toolUse, "signature-sensitive stage must downgrade tool_use")
}

func TestIsAnthropicThinkingCannotBeModifiedError(t *testing.T) {
	require.True(t, isAnthropicThinkingCannotBeModifiedError([]byte(
		`{"error":{"message":"messages.44.content.0: thinking or redacted_thinking blocks in the latest assistant message cannot be modified."}}`)))
	require.False(t, isAnthropicThinkingCannotBeModifiedError([]byte(
		`{"error":{"message":"Invalid signature in thinking block"}}`)))
}
