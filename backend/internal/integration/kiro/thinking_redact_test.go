//go:build unit

package kiro

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractThinkingFromContent(t *testing.T) {
	visible, thinking := ExtractThinkingFromContent(
		"<thinking>internal</thinking>Hello",
	)
	require.Equal(t, "Hello", visible)
	require.Equal(t, "internal", thinking)
}

func TestRedactedThinkingData_DeterministicOpaque(t *testing.T) {
	a := RedactedThinkingData("reasoning text")
	b := RedactedThinkingData("reasoning text")
	c := RedactedThinkingData("other")
	require.NotEmpty(t, a)
	require.Equal(t, a, b)
	require.NotEqual(t, a, c)
	require.NotContains(t, a, "reasoning")
}

func TestKiroToClaudeResponse_RedactsThinking(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer", "secret reasoning", false, nil, 10, 5, "claude-sonnet-4-6",
	)
	require.Len(t, resp.Content, 2)
	require.Equal(t, "redacted_thinking", resp.Content[0].Type)
	require.Equal(t, RedactedThinkingData("secret reasoning"), resp.Content[0].Data)
	require.Empty(t, resp.Content[0].Thinking)
	require.Equal(t, "text", resp.Content[1].Type)
	require.Equal(t, "answer", resp.Content[1].Text)
}

func TestKiroToClaudeResponse_NoThinkingOmitsRedactedBlock(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer only", "", false, nil, 10, 5, "claude-opus-4-8",
	)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
}
