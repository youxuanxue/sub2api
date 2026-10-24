//go:build unit

package kiro

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func kiroHistoryAssistant(t *testing.T, payload *KiroPayload) *KiroAssistantResponseMessage {
	t.Helper()
	for i := range payload.ConversationState.History {
		if a := payload.ConversationState.History[i].AssistantResponseMessage; a != nil {
			return a
		}
	}
	t.Fatal("missing assistant history message")
	return nil
}

func TestClaudeToKiro_RewritesLongToolNamesInPromptBody(t *testing.T) {
	longName := "mcp__fixture__" + strings.Repeat("read_path_segment_", 6)
	require.Greater(t, len(longName), 64)
	short := shortenToolName(sanitizeToolName(longName))
	require.Len(t, short, 64)
	require.NotEqual(t, longName, short)

	req := &ClaudeRequest{
		Model: "claude-opus-5",
		Tools: []ClaudeTool{{Name: longName, InputSchema: map[string]any{"type": "object"}}},
		Messages: []ClaudeMessage{
			{Role: "user", Content: "Call " + longName + " next"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "text", "text": "I will use " + longName},
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": longName, "input": map[string]any{"path": "a.txt"}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "ok from " + longName},
			}},
			{Role: "user", Content: "Continue with " + longName},
		},
	}
	payload := ClaudeToKiro(req, false)
	current := payload.ConversationState.CurrentMessage.UserInputMessage.Content
	require.Contains(t, current, short)
	require.NotContains(t, current, longName)
	require.Equal(t, longName, payload.ToolNameMap[short])

	assistant := kiroHistoryAssistant(t, payload)
	require.Contains(t, assistant.Content, short)
	require.NotContains(t, assistant.Content, longName)

	// Tool result bodies keep the original name; only prompt text is rewritten.
	var resultText string
	collect := func(msg *KiroUserInputMessage) {
		if msg == nil || msg.UserInputMessageContext == nil {
			return
		}
		for _, tr := range msg.UserInputMessageContext.ToolResults {
			for _, c := range tr.Content {
				resultText += c.Text
			}
		}
	}
	for i := range payload.ConversationState.History {
		collect(payload.ConversationState.History[i].UserInputMessage)
	}
	collect(&payload.ConversationState.CurrentMessage.UserInputMessage)
	require.Contains(t, resultText, longName, "tool result body must keep the original name")
}

func TestClaudeToKiro_StripsReadPagesKeepsOtherTools(t *testing.T) {
	req := &ClaudeRequest{
		Model: "claude-opus-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "inspect"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": "toolu_r", "name": "Read", "input": map[string]any{
					"file_path": "/tmp/a.txt",
					"pages":     []any{1, 2},
				}},
				map[string]any{"type": "tool_use", "id": "toolu_o", "name": "Other", "input": map[string]any{
					"pages": []any{9},
					"q":     "keep",
				}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_r", "content": "a"},
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_o", "content": "b"},
			}},
		},
	}
	payload := ClaudeToKiro(req, false)
	assistant := kiroHistoryAssistant(t, payload)
	require.Len(t, assistant.ToolUses, 2)
	require.Equal(t, "/tmp/a.txt", assistant.ToolUses[0].Input["file_path"])
	_, hasPages := assistant.ToolUses[0].Input["pages"]
	require.False(t, hasPages, "Read.pages must be stripped before Kiro")
	require.Equal(t, []any{9}, assistant.ToolUses[1].Input["pages"])
	require.Equal(t, "keep", assistant.ToolUses[1].Input["q"])
}

func TestClaudeToKiro_RejectsInvalidToolJSONAndCanonicalizesIDs(t *testing.T) {
	weirdID := "tool use/with spaces!"
	req := &ClaudeRequest{
		Model: "claude-opus-5",
		Messages: []ClaudeMessage{
			{Role: "user", Content: "go"},
			{Role: "assistant", Content: []any{
				map[string]any{"type": "tool_use", "id": weirdID, "name": "Read", "input": "{not-json"},
				map[string]any{"type": "tool_use", "id": weirdID, "name": "Read", "input": map[string]any{"file_path": "ok.txt"}},
			}},
			{Role: "user", Content: []any{
				map[string]any{"type": "tool_result", "tool_use_id": weirdID, "content": "ok"},
			}},
		},
	}
	payload := ClaudeToKiro(req, false)
	assistant := kiroHistoryAssistant(t, payload)
	require.Len(t, assistant.ToolUses, 1, "invalid tool JSON must not become empty input")
	wantID := canonicalKiroToolUseID(weirdID)
	require.Equal(t, wantID, assistant.ToolUses[0].ToolUseID)
	require.NotEqual(t, weirdID, wantID)
	require.True(t, kiroToolUseIDPattern.MatchString(wantID))

	results := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults
	require.NotEmpty(t, results)
	require.Equal(t, wantID, results[0].ToolUseID)
}
