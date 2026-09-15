//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

// Fixed migration boundaries: a V4.1 request must never silently select a V4
// snapshot, and continuing Pro service must not become a Flash alias.
func TestDeepSeekFlashSurfacePreservesProviderModelIdentity(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	official := floor.NewAPIChannelTypes["43"]
	require.Equal(t, "deepseek-flash", official["deepseek-flash"])
	require.Equal(t, "deepseek-v4-pro", official["deepseek-v4-pro"])
	require.NotContains(t, official, "deepseek-v4.1-flash", "the model version is not an accepted API name")
	for _, override := range floor.AccountOverrides {
		require.NotContains(t, override.ModelMapping, "deepseek-flash", override.BaseURL)
		require.NotContains(t, override.ModelMapping, "deepseek-v4.1-flash", override.BaseURL)
		for requested, target := range override.ModelMapping {
			if requested == "deepseek-v4-pro" || requested == "deepseek-v4-pro-0813" {
				require.NotContains(t, target, "flash", override.BaseURL)
			}
		}
	}
	display := tkServedModelsManifestDisplayPresetIDsByChannelType(newapiconstant.ChannelTypeDeepSeek)
	require.Contains(t, display, "deepseek-flash")
	require.Contains(t, display, "deepseek-v4-pro")
	require.NotContains(t, display, "deepseek-v4.1-flash")
}

func TestDeepSeekFlashFallbackKeepsFixedSnapshotPrice(t *testing.T) {
	billing := newTestBillingService()
	stable := billing.getFallbackPricing("deepseek-flash")
	require.NotNil(t, stable)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1), stable.InputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), stable.OutputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.02), stable.CacheReadPricePerToken, 1e-15)
	for _, snapshot := range []string{"deepseek-v4-flash-0731", "deepseek-ai/deepseek-v4-flash-0731"} {
		historical := billing.getFallbackPricing(snapshot)
		require.NotNil(t, historical)
		require.InDelta(t, tkCNYPerMTokToUSDPerToken(1.5), historical.InputPricePerToken, 1e-15)
		require.InDelta(t, tkCNYPerMTokToUSDPerToken(4.5), historical.OutputPricePerToken, 1e-15)
		require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.05), historical.CacheReadPricePerToken, 1e-15)
	}
	require.Nil(t, billing.getFallbackPricing("deepseek-v4.1-flash"))
	require.Nil(t, billing.getFallbackPricing("deepseek-flash-unknown"))
}
