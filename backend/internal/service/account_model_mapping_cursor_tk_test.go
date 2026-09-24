//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/stretchr/testify/require"
)

func TestCursorMappingFloorExcludesGPTWithoutChangingOtherProviders(t *testing.T) {
	account := cursorTestAccount()
	ids := NewAPIModelMappingPresetIDsForAccount(account)
	require.Contains(t, ids, "composer-2.5")
	require.Contains(t, ids, "claude-opus-5-5")
	require.NotContains(t, ids, "composer-2")
	for _, id := range ids {
		require.True(t, cursorServingModelAllowed(id), "Cursor preset should only serve allowlisted families, got %s", id)
		require.False(t, strings.HasPrefix(id, "gpt-"), "Cursor preset contains %s", id)
		require.False(t, strings.HasPrefix(id, "gemini-"), "Cursor preset contains %s", id)
		require.False(t, strings.HasPrefix(id, "glm-"), "Cursor preset contains %s", id)
		require.False(t, strings.HasPrefix(id, "kimi-"), "Cursor preset contains %s", id)
	}
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	scope := "account_override:" + normalizeAccountModelMappingOverrideScope(PlatformNewAPI, 14, cursor.AgentBaseURL)
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "gpt-")
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "gemini-")
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "glm-")
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "kimi-")
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "deepseek-")
	require.Contains(t, floor.ForbiddenModelMappingKeys[scope], "composer-2")
	require.NotContains(t, floor.ForbiddenModelMappingPrefixes[PlatformNewAPI], "gpt-")
	hasOpenAIGPT := false
	for id := range floor.Platforms[PlatformOpenAI] {
		hasOpenAIGPT = hasOpenAIGPT || strings.HasPrefix(id, "gpt-")
	}
	require.True(t, hasOpenAIGPT, "Cursor exclusion must not remove OpenAI GPT models")
	found := false
	for _, override := range floor.AccountOverrides {
		if override.BaseURL == cursor.AgentBaseURL {
			found = true
			require.Len(t, override.ModelMapping, len(ids))
			for _, id := range ids {
				require.Equal(t, id, override.ModelMapping[id])
			}
		}
	}
	require.True(t, found)
}

func TestCursorServingModelAllowedFamilies(t *testing.T) {
	require.True(t, cursorServingModelAllowed("claude-opus-5-5"))
	require.True(t, cursorServingModelAllowed("grok-4.6"))
	require.True(t, cursorServingModelAllowed("composer-2.5"))
	require.True(t, cursorServingModelAllowed("muse-spark-1.3"))
	require.False(t, cursorServingModelAllowed("composer-2"))
	require.False(t, cursorServingModelAllowed("gpt-5.4"))
	require.False(t, cursorServingModelAllowed("gemini-3.1-pro"))
	require.False(t, cursorServingModelAllowed("glm-5.2"))
	require.False(t, cursorServingModelAllowed("kimi-k3"))
	require.False(t, cursorServingModelAllowed("auto"))
	require.False(t, cursorServingModelAllowed(""))
}
