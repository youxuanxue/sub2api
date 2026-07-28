//go:build unit

package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureClaudeCodeCompletionGuard_AppendsExactlyOnce(t *testing.T) {
	prompt := "You are Claude Code."

	guarded := EnsureClaudeCodeCompletionGuard(prompt)
	guardedAgain := EnsureClaudeCodeCompletionGuard(guarded)

	require.Contains(t, guarded, "requested implementation and verification are complete")
	require.Contains(t, guarded, "If any item remains in_progress or pending, continue using tools")
	require.Equal(t, 1, strings.Count(guarded, ClaudeCodeCompletionGuardMarker))
	require.Equal(t, guarded, guardedAgain)
}

func TestEnsureClaudeCodeCompletionGuard_EmptyPromptStillReturnsGuard(t *testing.T) {
	require.Equal(t, ClaudeCodeCompletionGuardText, EnsureClaudeCodeCompletionGuard(""))
}
