//go:build unit

package kiro

import (
	"strings"
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

	visible, thinking := redactor.Push("<thinking>secret")
	require.Empty(t, visible)
	require.Equal(t, "secret", thinking)

	visible, thinking = redactor.Flush()
	require.Empty(t, visible)
	require.Empty(t, thinking)
}

func TestInlineThinkingRedactor_FlushRedactsLeadingTagFragment(t *testing.T) {
	var redactor InlineThinkingRedactor

	visible, thinking := redactor.Push("<thin")
	require.Empty(t, visible)
	require.Empty(t, thinking)

	visible, thinking = redactor.Flush()
	require.Empty(t, visible)
	require.Equal(t, "<thin", thinking)
}

func TestKiroToClaudeResponse_EmitsThinkingWithPlaceholderSignature(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer", "secret reasoning", false, nil, 10, 5, "claude-sonnet-4-6", "end_turn",
	)
	require.Len(t, resp.Content, 2)
	require.Equal(t, "thinking", resp.Content[0].Type)
	require.Equal(t, "secret reasoning", resp.Content[0].Thinking)
	require.True(t, strings.HasPrefix(resp.Content[0].Signature, KiroClientThinkingSignaturePrefix))
	require.NotContains(t, resp.Content[0].Signature, "secret reasoning")
	require.Equal(t, "text", resp.Content[1].Type)
	require.Equal(t, "answer", resp.Content[1].Text)
}

func TestKiroToClaudeResponse_OmitsPlaceholderReasoning(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer", "...", false, nil, 10, 5, "claude-sonnet-4-6", "end_turn",
	)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
}

func TestKiroToClaudeResponse_NoThinkingOmitsRedactedBlock(t *testing.T) {
	resp := KiroToClaudeResponse(
		"answer only", "", false, nil, 10, 5, "claude-opus-4-8", "end_turn",
	)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
}

// These inputs are assistant answer text, not the leading inline reasoning
// envelope. Chunk boundaries must never turn a literal into hidden reasoning.
func TestInlineThinkingRedactor_PreservesAnswerLiteralsAcrossChunks(t *testing.T) {
	cases := []struct {
		name     string
		wire     string
		visible  string
		thinking string
	}{
		{"unclosed_literal", "Explain <thinking> and keep the rest of this answer", "Explain <thinking> and keep the rest of this answer", ""},
		{"closed_literal", "Use <thinking>example</thinking> in this example", "Use <thinking>example</thinking> in this example", ""},
		{"inline_code", "`<thinking>` is a literal tag", "`<thinking>` is a literal tag", ""},
		{"fenced_code", "```xml\n<thinking>example\n```\nanswer", "```xml\n<thinking>example\n```\nanswer", ""},
		{"partial_literal", "The prefix is <thin", "The prefix is <thin", ""},
		{"less_than", "a < b and a trailing <", "a < b and a trailing <", ""},
		{"after_reasoning", "<thinking>private</thinking>Explain <thinking>literally", "Explain <thinking>literally", "private"},
		{"leading_reasoning", "\n<thinking>private</thinking>answer", "\nanswer", "private"},
		{"unclosed_leading_reasoning", "<thinking>private tail", "", "private tail"},
		{"incomplete_leading_envelope", "<thin", "", "<thin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for split := 0; split <= len(tc.wire); split++ {
				var r InlineThinkingRedactor
				var visible, thinking strings.Builder
				for _, chunk := range []string{tc.wire[:split], tc.wire[split:]} {
					v, th := r.Push(chunk)
					visible.WriteString(v)
					thinking.WriteString(th)
				}
				v, th := r.Flush()
				visible.WriteString(v)
				thinking.WriteString(th)
				require.Equal(t, tc.visible, visible.String(), "split=%d", split)
				require.Equal(t, tc.thinking, thinking.String(), "split=%d", split)
			}
		})
	}
}
