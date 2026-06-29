//go:build unit

package kiro

import (
	"strings"
	"testing"

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
