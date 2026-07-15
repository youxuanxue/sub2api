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

func TestInlineThinkingRedactor_SplitTagsAcrossChunks(t *testing.T) {
	var redactor InlineThinkingRedactor

	visible, thinking := redactor.Push("<thin")
	require.Empty(t, visible)
	require.Empty(t, thinking)

	visible, thinking = redactor.Push("king>secret")
	require.Empty(t, visible)
	require.Equal(t, "secret", thinking)

	visible, thinking = redactor.Push(" plan</thin")
	require.Empty(t, visible)
	require.Equal(t, " plan", thinking)

	visible, thinking = redactor.Push("king>Visible")
	require.Equal(t, "Visible", visible)
	require.Empty(t, thinking)

	visible, thinking = redactor.Flush()
	require.Empty(t, visible)
	require.Empty(t, thinking)
}

func TestInlineThinkingRedactor_UnclosedThinkingFlushesAsThinking(t *testing.T) {
	var redactor InlineThinkingRedactor

	visible, thinking := redactor.Push("prefix <thinking>secret")
	require.Equal(t, "prefix ", visible)
	require.Equal(t, "secret", thinking)

	visible, thinking = redactor.Flush()
	require.Empty(t, visible)
	require.Empty(t, thinking)
}

func TestInlineThinkingRedactor_FlushRedactsTrailingTagFragment(t *testing.T) {
	var redactor InlineThinkingRedactor

	visible, thinking := redactor.Push("visible <thin")
	require.Equal(t, "visible ", visible)
	require.Empty(t, thinking)

	visible, thinking = redactor.Flush()
	require.Empty(t, visible)
	require.Equal(t, "<thin", thinking)
}

func TestKiroToClaudeResponse_OmitsUnsignedThinking(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer", "secret reasoning", false, nil, 10, 5, "claude-sonnet-4-6",
	)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
	require.Equal(t, "answer", resp.Content[0].Text)
}

func TestKiroToClaudeResponse_NoThinkingOmitsRedactedBlock(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer only", "", false, nil, 10, 5, "claude-opus-4-8",
	)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
}
