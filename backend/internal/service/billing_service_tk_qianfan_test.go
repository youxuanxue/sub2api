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
	require.Equal(t, "deepseek-v4-pro", tkQianfanScopedBillingModel("deepseek-v4-pro", official))
	require.Equal(t, "deepseek-v3.2", tkQianfanScopedBillingModel("deepseek-v3.2", qianfan))
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
}
