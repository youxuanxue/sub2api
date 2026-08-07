//go:build unit

package service

import (
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestTkQianfanScopedBillingModel(t *testing.T) {
	t.Parallel()
	qianfan := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanBaseURL,
		},
	}
	official := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDeepSeek,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}

	require.Equal(t, "deepseek-v4-pro.qianfan", tkQianfanScopedBillingModel("deepseek-v4-pro", qianfan))
	require.Equal(t, "deepseek-v4-flash.qianfan", tkQianfanScopedBillingModel("deepseek-v4-flash", qianfan))
	require.Equal(t, "glm-5.2.qianfan", tkQianfanScopedBillingModel("glm-5.2", qianfan))
	require.Equal(t, "kimi-k2.6.qianfan", tkQianfanScopedBillingModel("kimi-k2.6", qianfan))
	require.Equal(t, "deepseek-v4-pro", tkQianfanScopedBillingModel("deepseek-v4-pro", official))
	require.Equal(t, "deepseek-v3.2", tkQianfanScopedBillingModel("deepseek-v3.2", qianfan))
}

func TestTkQianfanOverlay_DeepSeekV32UsesTieredIntervals(t *testing.T) {
	t.Parallel()
	entry := loadTKPricingOverlay()["deepseek-v3.2"]
	require.NotNil(t, entry)
	require.Len(t, entry.Intervals, 2)
	require.NotNil(t, entry.Intervals[0].MaxTokens)
	require.Equal(t, 32000, *entry.Intervals[0].MaxTokens)
	require.Nil(t, entry.Intervals[1].MaxTokens)
	require.InDelta(t, 2.9850746268656716e-07, entry.InputCostPerToken, 1e-15)
	require.InDelta(t, 5.970149253731343e-07, *entry.Intervals[1].InputPrice, 1e-15)
}

func TestTkQianfanOverlay_ThinkSKUUsesIntervalOutputNotFlatThinkingRate(t *testing.T) {
	t.Parallel()
	entry := loadTKPricingOverlay()["deepseek-v3.2-think"]
	require.NotNil(t, entry)
	require.Zero(t, entry.ThinkingOutputCostPerToken,
		"dedicated think SKU with intervals must not pin a flat thinking_output rate")
	require.Len(t, entry.Intervals, 2)
	require.NotNil(t, entry.Intervals[1].OutputPrice)
	require.InDelta(t, 8.955223880597015e-07, *entry.Intervals[1].OutputPrice, 1e-15)
}

func TestTkQianfanScopedBillingModel_UsesQianfanRatesInCost(t *testing.T) {
	t.Parallel()
	svc := newTestBillingService()
	qianfan := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanBaseURL,
		},
	}
	billingModel := tkQianfanScopedBillingModel("deepseek-v4-pro", qianfan)
	require.Equal(t, "deepseek-v4-pro.qianfan", billingModel)

	qianfanOwner := loadTKPricingOverlay()["deepseek-v4-pro.qianfan"]
	officialOwner := loadTKPricingOverlay()["deepseek-v4-pro"]
	require.NotNil(t, qianfanOwner)
	require.NotNil(t, officialOwner)
	require.Greater(t, qianfanOwner.InputCostPerToken, officialOwner.InputCostPerToken)
	require.Greater(t, qianfanOwner.OutputCostPerToken, officialOwner.OutputCostPerToken)

	pricing, err := svc.GetModelPricing(billingModel)
	require.NoError(t, err)
	officialPricing, err := svc.GetModelPricing("deepseek-v4-pro")
	require.NoError(t, err)
	require.Greater(t, pricing.InputPricePerToken, officialPricing.InputPricePerToken)
}

func TestTkQianfanScopedBillingModel_ExcludesDeepSeekPeakValley(t *testing.T) {
	t.Parallel()
	policy := loadTkDeepSeekPeakValleyPolicy()
	require.NotNil(t, policy)
	require.False(t, tkDeepSeekPeakValleyAppliesWithPolicy(policy, "deepseek-v4-pro.qianfan", PricingSourceLiteLLM))

	at := time.Date(2026, 7, 21, 10, 0, 0, 0, timezone.Location())
	base := &ModelPricing{InputPricePerToken: 0.012, OutputPricePerToken: 0.024}
	scaled := tkApplyDeepSeekPeakValleyPricing("deepseek-v4-pro.qianfan", base, at, PricingSourceLiteLLM)
	require.InDelta(t, base.InputPricePerToken, scaled.InputPricePerToken, 1e-12)
	require.InDelta(t, base.OutputPricePerToken, scaled.OutputPricePerToken, 1e-12)
	require.False(t, tkDeepSeekPeakValleyAppliesWithPolicy(policy, "deepseek-v3.2-think", PricingSourceLiteLLM))
}
