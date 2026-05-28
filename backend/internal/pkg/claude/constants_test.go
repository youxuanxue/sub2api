//go:build unit

package claude

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFullClaudeCodeMimicryBetas_MatchesCC0154SonnetCapture(t *testing.T) {
	want := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"extended-cache-ttl-2025-04-11",
		"cache-diagnosis-2026-04-07",
	}
	require.Equal(t, want, FullClaudeCodeMimicryBetas())
	require.Equal(t, want, splitBetaHeader(DefaultBetaHeader))
	require.NotContains(t, FullClaudeCodeMimicryBetas(), BetaEffort)
	require.NotContains(t, FullClaudeCodeMimicryBetas(), BetaFineGrainedToolStreaming)
}

func TestFullClaudeCodeHaikuMimicryBetas_MatchesCC0154HaikuCapture(t *testing.T) {
	want := []string{
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"structured-outputs-2025-12-15",
		"cache-diagnosis-2026-04-07",
	}
	require.Equal(t, want, FullClaudeCodeHaikuMimicryBetas())
	require.Equal(t, want, splitBetaHeader(HaikuBetaHeader))
	require.NotContains(t, FullClaudeCodeHaikuMimicryBetas(), BetaEffort)
	require.NotContains(t, FullClaudeCodeHaikuMimicryBetas(), BetaAdvancedToolUse)
	require.NotContains(t, FullClaudeCodeHaikuMimicryBetas(), BetaClaudeCode)
	require.NotContains(t, FullClaudeCodeHaikuMimicryBetas(), BetaExtendedCacheTTL)
}

func TestJoinBetaHeader(t *testing.T) {
	require.Equal(t, DefaultBetaHeader, JoinBetaHeader(FullClaudeCodeMimicryBetas()))
	require.Equal(t, HaikuBetaHeader, JoinBetaHeader(FullClaudeCodeHaikuMimicryBetas()))
}

func splitBetaHeader(header string) []string {
	parts := make([]string, 0)
	for _, p := range strings.Split(header, ",") {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}
