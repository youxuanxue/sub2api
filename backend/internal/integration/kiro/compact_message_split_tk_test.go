package kiro

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitClaudeCompactUserMessage_PreservesBlocksAsTurns(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "text", "text": strings.Repeat("a", 100*1024)},
		map[string]any{"type": "input_text", "text": strings.Repeat("b", 100*1024)},
		map[string]any{"type": "text", "text": strings.Repeat("c", 100*1024)},
	}
	req := &ClaudeRequest{
		MaxTokens:         64000,
		ContextManagement: json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015"}]}`),
		Messages: []ClaudeMessage{
			{Role: "user", Content: "compact preamble"},
			{Role: "user", Content: blocks},
		},
	}

	rep := SplitClaudeCompactUserMessage(req)
	require.True(t, rep.Candidate)
	require.True(t, rep.Applied, "skip=%s", rep.SkipReason)
	require.Equal(t, 3, rep.BlocksBefore)
	require.Equal(t, 4, len(req.Messages))
	require.Equal(t, "compact preamble", req.Messages[0].Content)
	require.Equal(t, strings.Repeat("a", 100*1024), req.Messages[1].Content)
	require.Equal(t, strings.Repeat("c", 100*1024), req.Messages[3].Content)
}

func TestSplitClaudeCompactUserMessage_RejectsClassifierAndMixedContent(t *testing.T) {
	base := []any{
		map[string]any{"type": "text", "text": strings.Repeat("x", 200*1024)},
		map[string]any{"type": "text", "text": strings.Repeat("y", 100*1024)},
	}

	classifier := &ClaudeRequest{MaxTokens: 64, Messages: []ClaudeMessage{{Role: "user", Content: base}}}
	rep := SplitClaudeCompactUserMessage(classifier)
	require.False(t, rep.Applied)
	require.Equal(t, "classifier_budget", rep.SkipReason)
	require.Len(t, classifier.Messages, 1)

	mixed := &ClaudeRequest{MaxTokens: 64000, ContextManagement: json.RawMessage(`{}`), Messages: []ClaudeMessage{{
		Role:    "user",
		Content: append(base, map[string]any{"type": "image", "source": map[string]any{"type": "base64"}}),
	}}}
	rep = SplitClaudeCompactUserMessage(mixed)
	require.False(t, rep.Applied)
	require.Equal(t, "mixed_content_blocks", rep.SkipReason)
	require.Len(t, mixed.Messages, 1)
}

func TestSplitClaudeCompactUserMessage_LeavesOrdinaryAssistantHistoryUntouched(t *testing.T) {
	req := &ClaudeRequest{
		MaxTokens:         64000,
		ContextManagement: json.RawMessage(`{}`),
		Messages: []ClaudeMessage{
			{Role: "assistant", Content: "previous response"},
			{Role: "user", Content: []any{
				map[string]any{"type": "text", "text": strings.Repeat("x", 200*1024)},
				map[string]any{"type": "text", "text": strings.Repeat("y", 100*1024)},
			}},
		},
	}

	rep := SplitClaudeCompactUserMessage(req)
	require.False(t, rep.Applied)
	require.Equal(t, "assistant_turn_present", rep.SkipReason)
	require.Len(t, req.Messages, 2)
}

func TestSplitClaudeCompactUserMessage_AllowsExistingHistoryTruncatorToFitWire(t *testing.T) {
	blocks := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": strings.Repeat(string(rune('a'+i%26)), 40*1024),
		})
	}
	req := &ClaudeRequest{
		Model:             "claude-opus-5",
		MaxTokens:         64000,
		ContextManagement: json.RawMessage(`{}`),
		System:            strings.Repeat("system ", 10*1024),
		Messages:          []ClaudeMessage{{Role: "user", Content: blocks}},
	}
	before := ClaudeToKiro(req, false)
	require.Error(t, ValidatePayloadSize(before))

	rep := SplitClaudeCompactUserMessage(req)
	require.True(t, rep.Applied, "skip=%s", rep.SkipReason)
	after := ClaudeToKiro(req, false)
	require.NoError(t, ValidatePayloadSize(after))
	require.Less(t, len(after.ConversationState.History), len(blocks))
	require.Contains(t, after.ConversationState.CurrentMessage.UserInputMessage.Content, strings.Repeat("d", 40*1024))
}
