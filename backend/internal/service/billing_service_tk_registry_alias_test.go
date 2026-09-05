//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUS043_GPT55ProAliasBillsRoutedRegistryOwner(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricingService := NewPricingService(&config.Config{}, nil)
	billing := NewBillingService(&config.Config{}, pricingService)

	declaredOwner, declared := tkPricingRegistryAliasOwner("gpt-5.5-pro")
	require.True(t, declared, "gpt-5.5-pro must be a declared _aliases owner, not a Go special case")
	require.Equal(t, "gpt-5.5", declaredOwner)

	owner := pricingService.GetModelPricing("gpt-5.5")
	require.NotNil(t, owner)
	pricing, err := billing.GetModelPricing("gpt-5.5-pro")
	require.NoError(t, err)
	require.InDelta(t, owner.InputCostPerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, owner.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15)
	require.InDelta(t, 5e-6, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 30e-6, pricing.OutputPricePerToken, 1e-15)
	require.False(t, billing.IsServedViaFamilyFloor("gpt-5.5-pro"),
		"declared public alias must not raise served_at_fallback")
}

func TestGPT56PublicAliasesBillSolOwner(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricingService := NewPricingService(&config.Config{}, nil)
	billing := NewBillingService(&config.Config{}, pricingService)

	sol := pricingService.GetModelPricing("gpt-5.6-sol")
	require.NotNil(t, sol)

	for _, alias := range []string{"gpt-5.6", "gpt-5.6-chat-latest"} {
		owner, declared := tkPricingRegistryAliasOwner(alias)
		require.Truef(t, declared, "%s must be overlay _aliases → gpt-5.6-sol (SSOT), not a duplicate price row", alias)
		require.Equal(t, "gpt-5.6-sol", owner)

		pricing, err := billing.GetModelPricing(alias)
		require.NoError(t, err, alias)
		require.InDelta(t, sol.InputCostPerToken, pricing.InputPricePerToken, 1e-15, alias)
		require.InDelta(t, sol.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15, alias)
		require.False(t, billing.IsServedViaFamilyFloor(alias),
			"declared public alias %s must not raise served_at_fallback", alias)
	}
}

func TestUS043_LegacyFallbackNumbersCannotAffectBilling(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	billing := NewBillingService(&config.Config{}, &PricingService{})
	legacy := billing.fallbackPrices["gemini-2.5-pro"]
	require.NotNil(t, legacy)
	legacy.InputPricePerToken = 0.99
	legacy.OutputPricePerToken = 0.99

	pricing, err := billing.GetModelPricing("gemini-future-pro")
	require.NoError(t, err)
	owner := loadTKPricingOverlay()["gemini-2.5-pro"]
	require.NotNil(t, owner)
	require.InDelta(t, owner.InputCostPerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, owner.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15)
	require.NotEqual(t, 0.99, pricing.InputPricePerToken)
}

func TestUS043_RegistryBackedLegacyMatcherKeepsExplicitOwner(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	billing := NewBillingService(&config.Config{}, &PricingService{})

	pricing := billing.getRegistryAliasPricing("deepseek-v4-flash-future")
	require.NotNil(t, pricing)
	snapshot := loadTKPricingOverlaySnapshot()
	owner := tkPresentLiteLLMModelPricingFromSnapshot(snapshot.Models["deepseek-v4-flash"], snapshot)
	require.NotNil(t, owner)
	require.InDelta(t, owner.InputCostPerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, owner.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15)
}

func TestUS043_RegistryAliasPriceAndPolicyUseOneSnapshot(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	envelope := registryEnvelopeForTest(t, func(registry map[string]any) {
		registry["deepseek-v4-flash"].(map[string]any)["input_cost_per_token"] = 1.0
		config := registry["_config"].(map[string]any)
		baseTax := config["official_list_base_tax"].(map[string]any)
		baseTax["multiplier"] = 1.5
	}, nil)
	rebuildTKOverlayUnion([]byte(envelope))
	billing := NewBillingService(&config.Config{}, &PricingService{})

	pricing := billing.getRegistryAliasPricing("deepseek-v4-flash-future")
	require.NotNil(t, pricing)
	require.Equal(t, "deepseek-v4-flash", pricing.registryOwner)
	require.InDelta(t, 1.5, pricing.InputPricePerToken, 1e-15)
}
