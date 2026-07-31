//go:build unit

package kiro

import (
	"testing"

	claudepkg "github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestConsumeClaudeCodeCompletionSignal_StripsOnlyPrivateTool(t *testing.T) {
	visible, signal := ConsumeClaudeCodeCompletionSignal([]KiroToolUse{
		{ToolUseID: "toolu_read", Name: "Read", Input: map[string]any{"file_path": "a.go"}},
		{ToolUseID: "toolu_done", Name: claudepkg.ClaudeCodeCompletionToolName, Input: map[string]any{
			"status":  "complete",
			"message": "Done and verified.",
		}},
	})

	require.Len(t, visible, 1)
	require.Equal(t, "Read", visible[0].Name)
	require.Equal(t, &ClaudeCodeCompletionSignal{Status: "complete", Message: "Done and verified."}, signal)
}

func TestPrepareClaudeCodeCompletionContinuation_PreservesToolsAndMovesTurnToHistory(t *testing.T) {
	payload := &KiroPayload{ClaudeCodeCompletionProtocol: true}
	payload.ConversationState.AgentContinuationId = "old-continuation"
	payload.ConversationState.History = []KiroHistoryMessage{
		{UserInputMessage: &KiroUserInputMessage{Content: "system priming", ModelID: "claude-sonnet-4.5", Origin: "AI_EDITOR"}},
		{AssistantResponseMessage: &KiroAssistantResponseMessage{Content: "I will follow these instructions."}},
	}
	completionTool, enabled := addClaudeCodeCompletionTool(nil)
	require.True(t, enabled)
	inputSchema := completionTool[0].ToolSpecification.InputSchema.JSON.(map[string]any)
	messageSchema := inputSchema["properties"].(map[string]any)["message"].(map[string]any)
	require.Equal(t, 1, messageSchema["minLength"])
	payload.ConversationState.CurrentMessage.UserInputMessage = KiroUserInputMessage{
		Content: "implement the fix",
		ModelID: "claude-sonnet-4.5",
		Origin:  "AI_EDITOR",
		UserInputMessageContext: &UserInputMessageContext{
			Tools: completionTool,
		},
	}

	PrepareClaudeCodeCompletionContinuation(payload, "I found the relevant file.")

	require.NotEqual(t, "old-continuation", payload.ConversationState.AgentContinuationId)
	require.Contains(t, payload.ConversationState.CurrentMessage.UserInputMessage.Content, "still active")
	require.Contains(t, payload.ConversationState.CurrentMessage.UserInputMessage.Content, "repair turn is transport-only")
	require.Contains(t, payload.ConversationState.CurrentMessage.UserInputMessage.Content, "Do not emit a recap")
	require.Contains(t, payload.ConversationState.CurrentMessage.UserInputMessage.Content, "reusing the preceding user-facing answer verbatim")
	require.Len(t, payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools, 1)
	require.Equal(t, claudepkg.ClaudeCodeCompletionToolName,
		payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification.Name)
	require.Equal(t, "implement the fix", payload.ConversationState.History[len(payload.ConversationState.History)-2].UserInputMessage.Content)
	require.Equal(t, "I found the relevant file.", payload.ConversationState.History[len(payload.ConversationState.History)-1].AssistantResponseMessage.Content)
}
