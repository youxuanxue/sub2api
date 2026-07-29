//go:build unit

package kiro

import (
	"strings"
	"testing"

	claudepkg "github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestApplyPromptFilters_ClaudeCodePreservesAnthropicIdentity(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# doing tasks",
		"# using your tools",
		"# tone and style",
	}, "\n")

	got := applyPromptFilters(ccPrompt)
	require.Contains(t, got, "You are Claude Code, Anthropic's official CLI for Claude.")
	require.Contains(t, got, "# doing tasks")
	require.NotContains(t, got, "backend for Claude Code CLI")
	require.NotEqual(t, claudeCodeBackendPrompt, got)
}

func TestApplyPromptFilters_ClaudeCodeStripsEnvNoise(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# Environment",
		"gitStatus: dirty",
		"# doing tasks",
		"# using your tools",
	}, "\n")

	got := applyPromptFilters(ccPrompt)
	require.Contains(t, got, "Anthropic's official CLI for Claude")
	require.NotContains(t, got, "gitStatus")
	require.NotContains(t, got, "# Environment")
}

func TestBuildClaudeSystemPrompt_AddsKiroIdentityOverrideWithoutSystemPrompt(t *testing.T) {
	got := buildClaudeSystemPrompt(nil, false)
	require.Contains(t, got, "You are Claude, Anthropic's assistant")
	require.Contains(t, got, "Do not identify as Kiro")
}

func TestBuildClaudeSystemPrompt_ClaudeCodePreservesPromptWithKiroIdentityOverride(t *testing.T) {
	ccPrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# doing tasks",
		"# using your tools",
	}, "\n")

	got := buildClaudeSystemPrompt([]interface{}{map[string]interface{}{"type": "text", "text": ccPrompt}}, false)
	require.Contains(t, got, "You are Claude, Anthropic's assistant")
	require.Contains(t, got, "Do not identify as Kiro")
	require.Contains(t, got, "You are Claude Code, Anthropic's official CLI for Claude.")
	require.Contains(t, got, "<sub2api-claude-code-todo-guard>")
	require.Contains(t, got, "requested implementation and verification are complete")
	require.Contains(t, got, "continue using tools")
	require.Equal(t, 1, strings.Count(got, "<sub2api-claude-code-todo-guard>"))
}

func TestBuildClaudeSystemPrompt_NonClaudeCodeDoesNotAddCompletionGuard(t *testing.T) {
	got := buildClaudeSystemPrompt("You are a concise support assistant.", false)
	require.NotContains(t, got, "<sub2api-claude-code-todo-guard>")
}

func TestUS041_ClaudeToKiro_CompletionGuardAppearsOnceInSystemPriming(t *testing.T) {
	basePrompt := strings.Join([]string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are an interactive agent that helps users with software engineering tasks.",
		"# doing tasks",
		"# using your tools",
	}, "\n")

	for _, tt := range []struct {
		name   string
		system string
	}{
		{name: "guard absent", system: basePrompt},
		{name: "guard already present", system: basePrompt + "\n\n" + claudepkg.ClaudeCodeCompletionGuardText},
	} {
		t.Run(tt.name, func(t *testing.T) {
			payload := ClaudeToKiro(&ClaudeRequest{
				Model:     "claude-opus-5",
				MaxTokens: 1024,
				System:    tt.system,
				Messages: []ClaudeMessage{
					{Role: "user", Content: "implement and verify the requested change"},
				},
			}, false)

			require.NotNil(t, payload)
			require.True(t, payload.ClaudeCodeCompletionProtocol)
			require.NotEmpty(t, payload.ConversationState.History)
			require.NotNil(t, payload.ConversationState.History[0].UserInputMessage)
			require.Contains(t, payload.ConversationState.History[0].UserInputMessage.Content, claudepkg.ClaudeCodeCompletionGuardMarker)

			var wireText strings.Builder
			for _, message := range payload.ConversationState.History {
				if message.UserInputMessage != nil {
					wireText.WriteString(message.UserInputMessage.Content)
				}
			}
			wireText.WriteString(payload.ConversationState.CurrentMessage.UserInputMessage.Content)
			require.Equal(t, 1, strings.Count(wireText.String(), claudepkg.ClaudeCodeCompletionGuardMarker))

			ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
			require.NotNil(t, ctx)
			completionTools := 0
			for _, tool := range ctx.Tools {
				if tool.ToolSpecification.Name == claudepkg.ClaudeCodeCompletionToolName {
					completionTools++
				}
			}
			require.Equal(t, 1, completionTools)
		})
	}
}

func TestClaudeToKiro_NonClaudeCodeDoesNotEnableCompletionProtocol(t *testing.T) {
	payload := ClaudeToKiro(&ClaudeRequest{
		Model:    "claude-sonnet-4-5",
		System:   "You are a concise support assistant.",
		Messages: []ClaudeMessage{{Role: "user", Content: "hello"}},
	}, false)

	require.False(t, payload.ClaudeCodeCompletionProtocol)
	ctx := payload.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext
	if ctx == nil {
		return
	}
	for _, tool := range ctx.Tools {
		require.NotEqual(t, claudepkg.ClaudeCodeCompletionToolName, tool.ToolSpecification.Name)
	}
}
