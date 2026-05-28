package claude

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetFullClaudeCodeMimicryBetasForContext_UsesResolver(t *testing.T) {
	SetClaudeCodeMimicryBetasResolver(nil)
	t.Cleanup(func() { SetClaudeCodeMimicryBetasResolver(nil) })

	SetClaudeCodeMimicryBetasResolver(func(context.Context) ([]string, []string, bool) {
		return []string{"runtime-sonnet"}, []string{"runtime-haiku"}, true
	})

	require.Equal(t, []string{"runtime-sonnet"}, GetFullClaudeCodeMimicryBetasForContext(context.Background()))
	require.Equal(t, []string{"runtime-haiku"}, GetFullClaudeCodeHaikuMimicryBetasForContext(context.Background()))
}

func TestGetFullClaudeCodeMimicryBetasForContext_FallsBackWhenResolverEmpty(t *testing.T) {
	SetClaudeCodeMimicryBetasResolver(func(context.Context) ([]string, []string, bool) {
		return nil, nil, false
	})
	t.Cleanup(func() { SetClaudeCodeMimicryBetasResolver(nil) })

	require.Equal(t, FullClaudeCodeMimicryBetas(), GetFullClaudeCodeMimicryBetasForContext(context.Background()))
	require.Equal(t, FullClaudeCodeHaikuMimicryBetas(), GetFullClaudeCodeHaikuMimicryBetasForContext(context.Background()))
}
