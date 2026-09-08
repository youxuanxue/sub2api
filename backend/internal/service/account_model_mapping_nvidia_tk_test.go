package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestNVIDIABuildMappingScope(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": newapiintegration.NVIDIABuildBaseURL},
	}
	ids := tkServedModelsManifestPresetIDsForSelector(account.Platform, account.ChannelType, account.GetBaseURL())
	require.NotEmpty(t, ids)
	require.ElementsMatch(t, ids, mapKeysForNVIDIATest(nvidiaBuildModelTargets), "every manifest row needs a wire target; targets must not invent serving intent")
	require.Equal(t, ids, NewAPIModelMappingPresetIDsForAccount(account))
	var displayIDs []string
	for _, id := range ids {
		if isCatalogModelRecommended(id) {
			displayIDs = append(displayIDs, id)
		}
	}
	require.Equal(t, displayIDs, NewAPIModelDisplayIDsForAccount(account))
	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, nvidiaBuildModelTargets, mapping)
	for _, target := range mapping {
		require.Contains(t, target, "/", "NVIDIA requires a provider namespace")
	}
	for _, base := range []string{"https://api.openai.com", "https://integrate.api.nvidia.com.evil.example", "http://integrate.api.nvidia.com", newapiintegration.NVIDIABuildBaseURL + "/v1"} {
		other := *account
		other.Credentials = map[string]any{"base_url": base}
		require.False(t, isNewAPINVIDIABuildAccount(&other))
		otherMapping, _ := accountModelMappingForAccount(context.Background(), &other, nil, nil, nil)
		require.NotEqual(t, mapping, otherMapping)
	}
	other := *account
	other.Platform = PlatformOpenAI
	require.False(t, isNewAPINVIDIABuildAccount(&other))
	other = *account
	other.ChannelType = newapiconstant.ChannelTypeDeepSeek
	require.False(t, isNewAPINVIDIABuildAccount(&other))
	for _, base := range []string{
		newapiintegration.NVIDIABuildBaseURL + "/",
		"HTTPS://INTEGRATE.API.NVIDIA.COM",
		" https://Integrate.Api.Nvidia.Com/ ",
	} {
		t.Run(base, func(t *testing.T) {
			variant := *account
			variant.Credentials = map[string]any{"base_url": base}
			require.Equal(t, ids, NewAPIModelMappingPresetIDsForAccount(&variant))
			require.Equal(t, displayIDs, NewAPIModelDisplayIDsForAccount(&variant))
			variantMapping, ok := accountModelMappingForAccount(context.Background(), &variant, nil, nil, nil)
			require.True(t, ok)
			require.Equal(t, mapping, variantMapping)
		})
	}
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	found := false
	for _, override := range floor.AccountOverrides {
		if override.BaseURL == newapiintegration.NVIDIABuildBaseURL {
			found = true
			require.Equal(t, mapping, override.ModelMapping)
		}
	}
	require.True(t, found, "activation bundle must carry the NVIDIA property scope")
}

func mapKeysForNVIDIATest(mapping map[string]string) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	return keys
}
