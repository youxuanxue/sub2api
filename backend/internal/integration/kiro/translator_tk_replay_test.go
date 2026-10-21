//go:build unit

package kiro

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestClaudeToKiro_PreservesInConversationSystemContext(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-opus-4-8", System: "top level policy", Messages: []ClaudeMessage{
		{Role: "user", Content: "start project"},
		{Role: "assistant", Content: "completed step one"},
		{Role: "system", Content: []any{map[string]any{"type": "text", "text": "step one is saved; continue step two"}}},
	}}
	payload := ClaudeToKiro(req, false)
	current := payload.ConversationState.CurrentMessage.UserInputMessage
	require.Contains(t, current.Content, "step one is saved; continue step two")
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(raw), "completed step one")
	require.Contains(t, string(raw), "top level policy")
}

func TestClaudeToKiro_EmptyTrailingControlKeepsCurrentUserTurn(t *testing.T) {
	req := &ClaudeRequest{Model: "claude-opus-4-8", Messages: []ClaudeMessage{{Role: "user", Content: "continue the current task"}, {Role: "system", Content: []any{}}}}
	payload := ClaudeToKiro(req, false)
	require.Equal(t, "continue the current task", payload.ConversationState.CurrentMessage.UserInputMessage.Content)
}

func TestConvertClaudeTools_DescriptionTruncationPreservesUTF8(t *testing.T) {
	desc := strings.Repeat("中", maxToolDescLen/3+1)
	tools, _ := convertClaudeTools([]ClaudeTool{{Name: "Read", Description: desc, InputSchema: map[string]any{"type": "object"}}})
	require.True(t, utf8.ValidString(tools[0].ToolSpecification.Description))
	raw, err := json.Marshal(tools)
	require.NoError(t, err)
	var decoded []KiroToolWrapper
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, strings.Repeat("中", maxToolDescLen/3)+"...", decoded[0].ToolSpecification.Description)
}
