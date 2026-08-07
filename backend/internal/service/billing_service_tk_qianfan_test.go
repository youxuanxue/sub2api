package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
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
