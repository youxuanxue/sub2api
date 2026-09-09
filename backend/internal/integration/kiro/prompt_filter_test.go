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
	require.NotContains(t, got, claudepkg.ClaudeCodeCompletionGuardMarker)
}

func TestBuildClaudeSystemPrompt_NonClaudeCodeDoesNotAddCompletionGuard(t *testing.T) {
	got := buildClaudeSystemPrompt("You are a concise support assistant.", false)
	require.NotContains(t, got, "<sub2api-claude-code-todo-guard>")
}

// The gateway must preserve client intent, without injecting business completion
// instructions or tools. Client-supplied instructions remain client-owned.
func TestClaudeToKiro_NoPrivateCompletionProtocol(t *testing.T) {
	base := "You are Claude Code, Anthropic's official CLI for Claude.\n# doing tasks\n# using your tools"
	for _, system := range []string{base, base + "\n" + claudepkg.ClaudeCodeCompletionGuardText, "You are a concise support assistant."} {
		payload := ClaudeToKiro(&ClaudeRequest{
			Model: "claude-opus-5", System: system,
			Messages: []ClaudeMessage{{Role: "user", Content: "Answer only YES or NO. Do not call tools."}},
			Tools:    []ClaudeTool{{Name: "Read", Description: "Read a file", InputSchema: map[string]interface{}{"type": "object"}}},
		}, false)
		current := payload.ConversationState.CurrentMessage.UserInputMessage
		require.Equal(t, "Answer only YES or NO. Do not call tools.", current.Content)
		require.NotNil(t, current.UserInputMessageContext)
		require.Len(t, current.UserInputMessageContext.Tools, 1)
		require.NotEqual(t, claudepkg.ClaudeCodeCompletionToolName, current.UserInputMessageContext.Tools[0].ToolSpecification.Name)
		priming := payload.ConversationState.History[0].UserInputMessage.Content
		require.Equal(t, strings.Count(system, claudepkg.ClaudeCodeCompletionGuardMarker), strings.Count(priming, claudepkg.ClaudeCodeCompletionGuardMarker))
		require.NotContains(t, priming, "Continue the same task now")
	}
}
