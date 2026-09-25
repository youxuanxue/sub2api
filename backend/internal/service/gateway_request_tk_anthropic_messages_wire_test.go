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

func TestTkPrepareAnthropicMessagesWireBody_RepairsTrailingThinking(t *testing.T) {
	// Prod 2026-09-25 user16: messages.N ends with thinking →
	// "The final block in an assistant message cannot be `thinking`".
	input := []byte(`{
		"model":"claude-fable-5",
		"thinking":{"type":"adaptive"},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q1"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"partial"},
				{"type":"thinking","thinking":"trailing","signature":"EuAG_stale"}
			]},
			{"role":"user","content":[{"type":"text","text":"continue"}]}
		]
	}`)

	out := tkPrepareAnthropicMessagesWireBody(nil, input, "claude-fable-5")
	require.NotEqual(t, string(input), string(out))

	content := gjson.GetBytes(out, "messages.1.content")
	require.True(t, content.IsArray())
	require.GreaterOrEqual(t, len(content.Array()), 1)
	last := content.Array()[len(content.Array())-1]
	require.NotEqual(t, "thinking", last.Get("type").String())
	require.NotEqual(t, "redacted_thinking", last.Get("type").String())
	thinking, _, text := countContentTypes(content)
	require.Equal(t, 0, thinking)
	require.GreaterOrEqual(t, text, 1)
}

func TestTkPrepareAnthropicMessagesWireBody_ThinkingOnlyHistoricalBecomesText(t *testing.T) {
	input := []byte(`{
		"model":"claude-fable-5",
		"thinking":{"type":"adaptive"},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q1"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"alone","signature":"EuAG_stale"}
			]},
			{"role":"user","content":[{"type":"text","text":"ok"}]}
		]
	}`)
	out := tkPrepareAnthropicMessagesWireBody(nil, input, "claude-fable-5")
	content := gjson.GetBytes(out, "messages.1.content")
	thinking, _, text := countContentTypes(content)
	require.Equal(t, 0, thinking)
	require.Equal(t, 1, text)
	require.Equal(t, "alone", content.Array()[0].Get("text").String())
}

func TestTkRectifyAnthropicThinkingContract400_FinalBlockThinking(t *testing.T) {
	body := []byte(`{
		"model":"claude-fable-5",
		"thinking":{"type":"adaptive"},
		"messages":[
			{"role":"user","content":[{"type":"text","text":"q1"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"alone","signature":"EuAG_stale"}
			]},
			{"role":"user","content":[{"type":"text","text":"ok"}]}
		]
	}`)
	respBody := []byte("{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"messages.1: The final block in an assistant message cannot be `thinking`.\"}}")

	out, kind, ok := tkRectifyAnthropicThinkingContract400(body, "claude-fable-5", respBody)
	require.True(t, ok)
	require.Equal(t, "final_block_thinking_repair", kind)
	thinking, _, text := countContentTypes(gjson.GetBytes(out, "messages.1.content"))
	require.Equal(t, 0, thinking)
	require.Equal(t, 1, text)
}

func TestIsAnthropicFinalBlockThinkingError(t *testing.T) {
	require.True(t, isAnthropicFinalBlockThinkingError([]byte(
		"{\"error\":{\"message\":\"messages.295: The final block in an assistant message cannot be `thinking`.\"}}")))
	require.True(t, isAnthropicThinkingContractErrorMessage(
		"messages.295: The final block in an assistant message cannot be `thinking`."))
	require.False(t, isAnthropicFinalBlockThinkingError([]byte(
		`{"error":{"message":"thinking blocks cannot be modified"}}`)))
}
