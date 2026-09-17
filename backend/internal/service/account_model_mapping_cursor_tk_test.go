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
	require.NotContains(t, ids, "composer-2")
	for _, id := range ids {
		require.False(t, strings.HasPrefix(id, "gpt-"), "Cursor preset contains %s", id)
	}
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	scope := "account_override:" + normalizeAccountModelMappingOverrideScope(PlatformNewAPI, 14, cursor.AgentBaseURL)
	require.Contains(t, floor.ForbiddenModelMappingPrefixes[scope], "gpt-")
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
