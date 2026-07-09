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
	p := &LiteLLMModelPricing{
		LiteLLMProvider:    "deepseek",
		InputCostPerToken:  in,
		OutputCostPerToken: out,
	}
	taxed := tkPresentLiteLLMModelPricing(p)
	require.NotSame(t, p, taxed, "taxed lookup must clone, not mutate cache")
	assert.InDelta(t, in*tkOfficialListBaseTaxMultiplier, taxed.InputCostPerToken, 1e-12)
	assert.InDelta(t, out*tkOfficialListBaseTaxMultiplier, taxed.OutputCostPerToken, 1e-12)
	assert.InDelta(t, in, p.InputCostPerToken, 1e-12, "cache entry unchanged")

	openai := &LiteLLMModelPricing{
		LiteLLMProvider:    "openai",
		InputCostPerToken:  in,
		OutputCostPerToken: out,
	}
	assert.Same(t, openai, tkPresentLiteLLMModelPricing(openai))
}

func TestTkOfficialListBaseTax_PublicCatalogAndBillingStayAligned(t *testing.T) {
	vendor := "dashscope"
	catalog := PublicCatalogPricing{
		Currency:          "USD",
		InputPer1KTokens:  0.001,
		OutputPer1KTokens: 0.002,
		Tiers: []PublicCatalogTier{
			{MinTokens: 0, InputPer1KTokens: 0.001, OutputPer1KTokens: 0.002},
		},
	}
	tkApplyBaseTaxToPublicCatalogPricing(vendor, &catalog)
	assert.InDelta(t, 0.001*tkOfficialListBaseTaxMultiplier, catalog.InputPer1KTokens, 1e-12)
	assert.InDelta(t, 0.002*tkOfficialListBaseTaxMultiplier, catalog.OutputPer1KTokens, 1e-12)
	assert.InDelta(t, catalog.InputPer1KTokens, catalog.Tiers[0].InputPer1KTokens, 1e-12)

	mp := &ModelPricing{
		InputPricePerToken:  0.000001,
		OutputPricePerToken: 0.000002,
	}
	taxed := tkApplyBaseTaxToModelPricingClone(mp)
	require.NotNil(t, taxed)
	assert.InDelta(t, 0.000001*tkOfficialListBaseTaxMultiplier, taxed.InputPricePerToken, 1e-12)
	assert.InDelta(t, 0.000002*tkOfficialListBaseTaxMultiplier, taxed.OutputPricePerToken, 1e-12)
}

func TestTkInferBaseTaxProvider(t *testing.T) {
	assert.Equal(t, "deepseek", tkInferBaseTaxProvider("deepseek-chat"))
	assert.Equal(t, "dashscope", tkInferBaseTaxProvider("qwen-plus"))
	assert.Equal(t, "volcengine", tkInferBaseTaxProvider("doubao-seed-2-0-pro-260215"))
	assert.Equal(t, "zhipu", tkInferBaseTaxProvider("glm-4.7"))
}

func TestPricingService_GetModelPricing_AppliesBaseTaxOnLookup(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{
		"deepseek-v4-pro": {
			"input_cost_per_token": 1e-6,
			"output_cost_per_token": 2e-6,
			"litellm_provider": "deepseek",
			"mode": "chat"
		},
		"glm-5.2": {
			"input_cost_per_token": 3e-6,
			"output_cost_per_token": 4e-6,
			"litellm_provider": "zhipu",
			"mode": "chat"
		}
	}`))
	require.NoError(t, err)
	svc.pricingData = data

	got := svc.GetModelPricing("deepseek-v4-pro")
	require.NotNil(t, got)
	assert.InDelta(t, 1e-6*tkOfficialListBaseTaxMultiplier, got.InputCostPerToken, 1e-15)
	assert.InDelta(t, 2e-6*tkOfficialListBaseTaxMultiplier, got.OutputCostPerToken, 1e-15)
	assert.InDelta(t, 1e-6, data["deepseek-v4-pro"].InputCostPerToken, 1e-15, "cached map stays pre-tax")

	glm := svc.GetModelPricing("glm-5.2")
	require.NotNil(t, glm)
	assert.InDelta(t, 3e-6*tkOfficialListBaseTaxMultiplier, glm.InputCostPerToken, 1e-15)
	assert.InDelta(t, 4e-6*tkOfficialListBaseTaxMultiplier, glm.OutputCostPerToken, 1e-15)
}
