//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapAntigravityModel_LiveAccountAllowsOnlyLiveClaudeSubset(t *testing.T) {
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": modelMappingToAny(mapping),
		},
	}

	require.NotEmpty(t, MapAntigravityModel(account, AntigravityDefaultTestModelID))
	require.Equal(t, "claude-sonnet-4-6", MapAntigravityModel(account, "claude-sonnet-4-6"))
	require.Equal(t, "claude-opus-4-6-thinking", MapAntigravityModel(account, "claude-opus-4-6"))
	require.Empty(t, MapAntigravityModel(account, "claude-sonnet-4-5"))
	require.Empty(t, MapAntigravityModel(account, "claude-opus-4-8"))
	require.Empty(t, MapAntigravityModel(account, "gpt-oss-120b-medium"))
}

func TestAntigravityDefaultTestModelID_IsGeminiWire(t *testing.T) {
	require.True(t, len(AntigravityDefaultTestModelID) > len("gemini-"))
	require.Contains(t, AntigravityDefaultTestModelID, "gemini-")
}
