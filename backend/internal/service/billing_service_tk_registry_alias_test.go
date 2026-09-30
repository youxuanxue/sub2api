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

func TestGPT6PublicAliasBillsAstraOwner(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricingService := NewPricingService(&config.Config{}, nil)
	billing := NewBillingService(&config.Config{}, pricingService)

	astra := pricingService.GetModelPricing("gpt-6-astra")
	require.NotNil(t, astra)
	require.InDelta(t, 1e-5, astra.InputCostPerToken, 1e-15)
	require.InDelta(t, 5e-5, astra.OutputCostPerToken, 1e-15)

	owner, declared := tkPricingRegistryAliasOwner("gpt-6")
	require.True(t, declared, "gpt-6 must be overlay _aliases → gpt-6-astra (SSOT)")
	require.Equal(t, "gpt-6-astra", owner)

	pricing, err := billing.GetModelPricing("gpt-6")
	require.NoError(t, err)
	require.InDelta(t, astra.InputCostPerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, astra.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15)
	require.False(t, billing.IsServedViaFamilyFloor("gpt-6"),
		"declared public alias must not raise served_at_fallback")

	require.Equal(t, "gpt-6-astra", normalizeOpenAIBillingModel("gpt-6"))
	require.Equal(t, "gpt-6-sol", normalizeOpenAIBillingModel("gpt-6-sol"))
	require.Equal(t, "gpt-6-astra", normalizeOpenAIBillingModel("gpt-6-astra"))
	require.Equal(t, "gpt-6-astra", CanonicalizeOpenAICompatRoutingModel("gpt-6"))
	require.Equal(t, "gpt-6-sol", CanonicalizeOpenAICompatRoutingModel("gpt-6-sol"))
	require.Equal(t, "gpt-6-astra", CanonicalizeOpenAICompatRoutingModel("gpt-6-astra"))
	require.True(t, isOpenAIGPT6AstraModel("gpt-6-astra-20260901"))
	require.True(t, isOpenAIGPT6AstraModel("gpt-6"))
	require.False(t, isOpenAIGPT6AstraModel("gpt-6-other"))
}

func TestGPT61PublicAliasBillsSolOwner(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricingService := NewPricingService(&config.Config{}, nil)
	billing := NewBillingService(&config.Config{}, pricingService)

	sol := pricingService.GetModelPricing("gpt-6.1-sol")
	require.NotNil(t, sol)
	require.InDelta(t, 2e-6, sol.InputCostPerToken, 1e-15)
	require.InDelta(t, 10e-6, sol.OutputCostPerToken, 1e-15)
	require.InDelta(t, 0.1e-6, sol.CacheReadInputTokenCost, 1e-15)

	owner, declared := tkPricingRegistryAliasOwner("gpt-6.1")
	require.True(t, declared, "gpt-6.1 must be overlay _aliases → gpt-6.1-sol (SSOT)")
	require.Equal(t, "gpt-6.1-sol", owner)

	pricing, err := billing.GetModelPricing("gpt-6.1")
	require.NoError(t, err)
	require.InDelta(t, sol.InputCostPerToken, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, sol.OutputCostPerToken, pricing.OutputPricePerToken, 1e-15)
	require.False(t, billing.IsServedViaFamilyFloor("gpt-6.1"),
		"declared public alias must not raise served_at_fallback")

	require.Equal(t, "gpt-6.1-sol", normalizeOpenAIBillingModel("gpt-6.1"))
	require.Equal(t, "gpt-6.1-sol", normalizeOpenAIBillingModel("gpt-6.1-sol"))
	require.Equal(t, "gpt-6.1-sol", CanonicalizeOpenAICompatRoutingModel("gpt-6.1"))
	require.Equal(t, "gpt-6.1-sol", CanonicalizeOpenAICompatRoutingModel("gpt-6.1-sol"))
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

func TestGeminiWebWireModelsAliasToPublicOwners(t *testing.T) {
	resetPricingRegistrySnapshot(t)
	pricingService := NewPricingService(&config.Config{}, nil)
	billing := NewBillingService(&config.Config{}, pricingService)

	// Live edge gemini-web account model_mapping (us3/us4/us5/us6) maps request
	// models onto exactly these three Worker wire ids as billing keys.
	cases := []struct {
		alias string
		owner string
	}{
		{"gemini-web-flash", "gemini-3.8-flash"},
		{"gemini-web-pro", "gemini-3.1-pro"},
		{"gemini-web-pro-image", "gemini-3.1-flash-image"},
	}
	for _, tc := range cases {
		owner, declared := tkPricingRegistryAliasOwner(tc.alias)
		require.Truef(t, declared, "%s must be overlay _aliases → %s", tc.alias, tc.owner)
		require.Equal(t, tc.owner, owner)

		want := pricingService.GetModelPricing(tc.owner)
		require.NotNil(t, want, tc.owner)
		got := pricingService.GetModelPricing(tc.alias)
		require.NotNil(t, got, tc.alias)
		require.InDelta(t, want.InputCostPerToken, got.InputCostPerToken, 1e-15, tc.alias)
		require.InDelta(t, want.OutputCostPerToken, got.OutputCostPerToken, 1e-15, tc.alias)
		require.InDelta(t, want.OutputCostPerImage, got.OutputCostPerImage, 1e-15, tc.alias)
		require.False(t, billing.IsServedViaFamilyFloor(tc.alias),
			"declared public alias %s must not raise served_at_fallback", tc.alias)
	}

	// gemini-web image traffic bills ImageCount with no invented usage tokens.
	cost := billing.CalculateImageCost("gemini-web-pro-image", "1K", 1, nil, 1)
	require.NotNil(t, cost)
	require.Positive(t, cost.TotalCost)
	require.InDelta(t, pricingService.GetModelPricing("gemini-3.1-flash-image").OutputCostPerImage, cost.TotalCost, 1e-12)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
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
