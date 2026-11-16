package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxCNMappingScope(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.MiniMaxCNBaseURL},
	}
	ids := tkServedModelsManifestPresetIDsForSelector(account.Platform, account.ChannelType, account.GetBaseURL())
	require.NotEmpty(t, ids)
	require.ElementsMatch(t, ids, mapKeysForMiniMaxCNTest(miniMaxCNModelTargets),
		"every manifest row needs a wire target; targets must not invent serving intent")
	require.Equal(t, ids, NewAPIModelMappingPresetIDsForAccount(account))
	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, miniMaxCNModelTargets, mapping)
	require.Equal(t, "MiniMax-M3.1-Flash", mapping["minimax-m3.1-flash"])

	for _, base := range []string{
		"https://api.minimax.io",
		"https://api.minimax.chat",
		"https://api.minimaxi.com.evil.example",
		newapiintegration.MiniMaxCNBaseURL + "/v1",
	} {
		other := *account
		other.Credentials = map[string]any{"base_url": base}
		require.False(t, isNewAPIMiniMaxCNAccount(&other))
		otherMapping, _ := accountModelMappingForAccount(context.Background(), &other, nil, nil, nil)
		require.NotEqual(t, mapping, otherMapping)
	}

	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	found := false
	for _, override := range floor.AccountOverrides {
		if override.BaseURL == newapiintegration.MiniMaxCNBaseURL {
			found = true
			require.Equal(t, mapping, override.ModelMapping)
		}
	}
	require.True(t, found, "activation bundle must carry the MiniMax CN property scope")
}

func mapKeysForMiniMaxCNTest(mapping map[string]string) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	return keys
}
