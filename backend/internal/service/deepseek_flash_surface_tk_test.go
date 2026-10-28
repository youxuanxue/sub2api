//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

// Fixed migration boundaries: DeepSeek identities stay on Volc and Qianfan
// plan floors. Ali Token Plan does not serve the series.
func TestDeepSeekFlashSurfacePreservesProviderModelIdentity(t *testing.T) {
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	official := floor.NewAPIChannelTypes["43"]
	require.Equal(t, "deepseek-flash", official["deepseek-flash"])
	require.Equal(t, "deepseek-v4-pro", official["deepseek-v4-pro"])
	for _, override := range floor.AccountOverrides {
		if override.ChannelType == newapiconstant.ChannelTypeAli && override.BaseURL == "https://token-plan.cn-beijing.maas.aliyuncs.com" {
			for _, model := range []string{
				"deepseek-chat",
				"deepseek-flash",
				"deepseek-v4-flash",
				"deepseek-v4-flash-0731",
				"deepseek-v4-pro",
				"deepseek-v4-pro-0813",
				"deepseek-v4.1-flash",
			} {
				require.NotContains(t, override.ModelMapping, model, override.BaseURL)
			}
		}
		if override.ChannelType == newapiconstant.ChannelTypeVolcEngine && override.BaseURL == "https://ark.cn-beijing.volces.com/api/plan/v3" {
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-flash"], override.BaseURL)
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-v4.1-flash"], override.BaseURL)
		}
		if override.ChannelType == newapiconstant.ChannelTypeBaiduV2 && override.BaseURL == "https://qianfan.baidubce.com/v2/tokenplan/personal" {
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-flash"], override.BaseURL)
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-v4.1-flash"], override.BaseURL)
			require.NotContains(t, override.ModelMapping, "deepseek-v4-flash")
		}
	}
	display := tkServedModelsManifestDisplayPresetIDsByChannelType(newapiconstant.ChannelTypeDeepSeek)
	require.Contains(t, display, "deepseek-flash", "official stable Flash name follows manifest display intent")
	require.Contains(t, display, "deepseek-v4-pro")
	require.NotContains(t, display, "deepseek-v4.1-flash")
}

func TestDeepSeekFlashFallbackSharesCurrentPrice(t *testing.T) {
	billing := newTestBillingService()
	stable := billing.getFallbackPricing("deepseek-flash")
	require.NotNil(t, stable)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1), stable.InputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), stable.OutputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.02), stable.CacheReadPricePerToken, 1e-15)
	for _, model := range []string{"deepseek-v4-flash", "deepseek-v4-flash-vision-exp", "deepseek-v4-flash-0731", "deepseek-ai/deepseek-v4-flash-0731", "deepseek-v4-flash-ga-260731"} {
		price := billing.getFallbackPricing(model)
		require.NotNil(t, price, model)
		require.Equal(t, stable.InputPricePerToken, price.InputPricePerToken, model)
		require.Equal(t, stable.OutputPricePerToken, price.OutputPricePerToken, model)
		require.Equal(t, stable.CacheReadPricePerToken, price.CacheReadPricePerToken, model)
	}
	v41, err := billing.GetModelPricing("deepseek-v4.1-flash")
	require.NoError(t, err)
	flashOwner, err := billing.GetModelPricing("deepseek-v4-flash")
	require.NoError(t, err)
	require.Equal(t, flashOwner.InputPricePerToken, v41.InputPricePerToken)
	require.Equal(t, flashOwner.OutputPricePerToken, v41.OutputPricePerToken)
	require.Equal(t, flashOwner.CacheReadPricePerToken, v41.CacheReadPricePerToken)
	require.Nil(t, billing.getFallbackPricing("deepseek-flash-unknown"))
}
