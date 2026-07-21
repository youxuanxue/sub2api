//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTkOfficialListBaseTax_AppliesToTargetProvidersOnly(t *testing.T) {
	in := 1.0
	out := 2.0
	policy := loadTkOfficialListBaseTaxPolicy()
	require.NoError(t, policy.validate())
	require.InDelta(t, 1.06, policy.Multiplier, 1e-12, "behavior-preserving migration from the former Go constant")
	for _, rule := range policy.Rules {
		p := &LiteLLMModelPricing{
			LiteLLMProvider:                     rule.Provider,
			InputCostPerToken:                   in,
			OutputCostPerToken:                  out,
			CacheCreationInputTokenCostPriority: 3,
		}
		taxed := tkPresentLiteLLMModelPricing(p)
		require.NotSame(t, p, taxed, rule.Provider+" lookup must clone, not mutate cache")
		assert.InDelta(t, in*policy.Multiplier, taxed.InputCostPerToken, 1e-12, rule.Provider)
		assert.InDelta(t, out*policy.Multiplier, taxed.OutputCostPerToken, 1e-12, rule.Provider)
		assert.InDelta(t, 3*policy.Multiplier, taxed.CacheCreationInputTokenCostPriority, 1e-12, rule.Provider)
		assert.InDelta(t, in, p.InputCostPerToken, 1e-12, rule.Provider+" cache entry changed")
	}

	openai := &LiteLLMModelPricing{
		LiteLLMProvider:    "openai",
		InputCostPerToken:  in,
		OutputCostPerToken: out,
	}
	assert.Same(t, openai, tkPresentLiteLLMModelPricing(openai))
}

func TestTkOfficialListBaseTax_PublicCatalogAndBillingStayAligned(t *testing.T) {
	policy := loadTkOfficialListBaseTaxPolicy()
	for _, rule := range policy.Rules {
		vendor := rule.Provider
		catalog := PublicCatalogPricing{
			Currency:          "USD",
			InputPer1KTokens:  0.001,
			OutputPer1KTokens: 0.002,
			Tiers: []PublicCatalogTier{
				{MinTokens: 0, InputPer1KTokens: 0.001, OutputPer1KTokens: 0.002},
			},
		}
		tkApplyBaseTaxToPublicCatalogPricing(vendor, &catalog)
		assert.InDelta(t, 0.001*policy.Multiplier, catalog.InputPer1KTokens, 1e-12, vendor)
		assert.InDelta(t, 0.002*policy.Multiplier, catalog.OutputPer1KTokens, 1e-12, vendor)
		assert.InDelta(t, catalog.InputPer1KTokens, catalog.Tiers[0].InputPer1KTokens, 1e-12, vendor)
	}

	mp := &ModelPricing{
		InputPricePerToken:                 0.000001,
		OutputPricePerToken:                0.000002,
		CacheCreationPricePerTokenPriority: 0.000003,
	}
	taxed := tkApplyBaseTaxToModelPricingClone(mp, tkOfficialListBaseTaxMultiplier())
	require.NotNil(t, taxed)
	assert.InDelta(t, 0.000001*tkOfficialListBaseTaxMultiplier(), taxed.InputPricePerToken, 1e-12)
	assert.InDelta(t, 0.000002*tkOfficialListBaseTaxMultiplier(), taxed.OutputPricePerToken, 1e-12)
	assert.InDelta(t, 0.000003*tkOfficialListBaseTaxMultiplier(), taxed.CacheCreationPricePerTokenPriority, 1e-12)
}

func TestTkInferBaseTaxProvider(t *testing.T) {
	policy := loadTkOfficialListBaseTaxPolicy()
	for _, rule := range policy.Rules {
		for _, prefix := range rule.ModelPrefixes {
			model := prefix + "ssot-probe"
			assert.Equal(t, rule.Provider, policy.inferProvider(model), model)
		}
		for _, fragment := range rule.ModelContains {
			model := "ssot-" + fragment + "-probe"
			assert.Equal(t, rule.Provider, policy.inferProvider(model), model)
		}
	}
	assert.Empty(t, policy.inferProvider("openai/gpt-5.4"))
}

func sampleModelForTaxRule(t *testing.T, rule tkOfficialListBaseTaxRule) string {
	t.Helper()
	if len(rule.ModelPrefixes) > 0 {
		return rule.ModelPrefixes[0] + "tax-policy-probe"
	}
	require.NotEmpty(t, rule.ModelContains)
	return "tax-policy-" + rule.ModelContains[0] + "-probe"
}

func TestPricingService_GetModelPricing_AppliesBaseTaxOnLookup(t *testing.T) {
	policy := loadTkOfficialListBaseTaxPolicy()
	data := make(map[string]*LiteLLMModelPricing, len(policy.Rules))
	for _, rule := range policy.Rules {
		model := sampleModelForTaxRule(t, rule)
		data[model] = &LiteLLMModelPricing{
			LiteLLMProvider:    rule.Provider,
			InputCostPerToken:  1e-6,
			OutputCostPerToken: 2e-6,
		}
	}
	svc := &PricingService{pricingData: data}

	for _, rule := range policy.Rules {
		model := sampleModelForTaxRule(t, rule)
		got := svc.GetModelPricing(model)
		require.NotNil(t, got, rule.Provider)
		assert.InDelta(t, 1e-6*policy.Multiplier, got.InputCostPerToken, 1e-15, rule.Provider)
		assert.InDelta(t, 2e-6*policy.Multiplier, got.OutputCostPerToken, 1e-15, rule.Provider)
		assert.InDelta(t, 1e-6, data[model].InputCostPerToken, 1e-15, rule.Provider+" cached map stays pre-tax")
	}
}

func TestBillingFallback_ConfiguredModelMatchersApplyBaseTax(t *testing.T) {
	base := &ModelPricing{InputPricePerToken: 1e-6, OutputPricePerToken: 2e-6}
	policy := loadTkOfficialListBaseTaxPolicy()
	for _, rule := range policy.Rules {
		model := sampleModelForTaxRule(t, rule)
		taxed := tkApplyOfficialListBaseTaxForModel(model, base)
		require.NotSame(t, base, taxed, model)
		assert.InDelta(t, 1e-6*policy.Multiplier, taxed.InputPricePerToken, 1e-15, model)
		assert.InDelta(t, 2e-6*policy.Multiplier, taxed.OutputPricePerToken, 1e-15, model)
	}
}
