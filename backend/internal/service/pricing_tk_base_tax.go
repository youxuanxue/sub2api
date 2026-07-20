package service

import "strings"

// tkOfficialListBaseTaxMultiplier is applied to official list prices for CN-origin
// providers whose upstream quotes exclude the base VAT layer TokenKey absorbs.
// Overlay JSON and the litellm mirror store pre-tax official rates; billing and
// /pricing apply this multiplier at presentation/resolution time so display matches
// charge. Channel (DB) pricing sits above this layer and is never re-taxed.
const tkOfficialListBaseTaxMultiplier = 1.06

type tkOfficialListBaseTaxRule struct {
	provider      string
	modelPrefixes []string
	modelContains []string
}

// tkOfficialListBaseTaxRules is the single owner for both provider/vendor tax
// eligibility and bare-model fallback classification. Keep row order stable so
// overlapping future model matchers resolve deterministically.
var tkOfficialListBaseTaxRules = []tkOfficialListBaseTaxRule{
	{provider: "deepseek", modelContains: []string{"deepseek"}},
	{provider: "dashscope", modelPrefixes: []string{"qwen"}},
	{provider: "moonshot", modelPrefixes: []string{"kimi-", "kimi/", "moonshot-"}},
	{provider: "volcengine", modelPrefixes: []string{"doubao", "seedream", "seedance"}},
	{provider: "zhipu", modelPrefixes: []string{"glm"}},
}

func tkLitellmProviderHasBaseTax(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, rule := range tkOfficialListBaseTaxRules {
		if provider == rule.provider {
			return true
		}
	}
	return false
}

// tkInferBaseTaxProvider maps a bare model id to a provider when only the model
// name is known (billing fallbackPrices path — those entries carry no vendor).
func tkInferBaseTaxProvider(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, rule := range tkOfficialListBaseTaxRules {
		for _, prefix := range rule.modelPrefixes {
			if strings.HasPrefix(m, prefix) {
				return rule.provider
			}
		}
		for _, fragment := range rule.modelContains {
			if strings.Contains(m, fragment) {
				return rule.provider
			}
		}
	}
	return ""
}

func tkApplyBaseTaxMultiplier(v float64) float64 {
	if v <= 0 {
		return v
	}
	return v * tkOfficialListBaseTaxMultiplier
}

func tkApplyBaseTaxToPricingInterval(iv PricingInterval) PricingInterval {
	out := iv
	if out.InputPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.InputPrice)
		out.InputPrice = &v
	}
	if out.OutputPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.OutputPrice)
		out.OutputPrice = &v
	}
	if out.CacheWritePrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.CacheWritePrice)
		out.CacheWritePrice = &v
	}
	if out.CacheReadPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.CacheReadPrice)
		out.CacheReadPrice = &v
	}
	if out.PerRequestPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.PerRequestPrice)
		out.PerRequestPrice = &v
	}
	return out
}

func tkApplyBaseTaxToPricingIntervals(intervals []PricingInterval) []PricingInterval {
	if len(intervals) == 0 {
		return intervals
	}
	out := make([]PricingInterval, len(intervals))
	for i := range intervals {
		out[i] = tkApplyBaseTaxToPricingInterval(intervals[i])
	}
	return out
}

func tkApplyBaseTaxToLiteLLMModelPricingClone(p *LiteLLMModelPricing) *LiteLLMModelPricing {
	if p == nil || !tkLitellmProviderHasBaseTax(p.LiteLLMProvider) {
		return p
	}
	c := *p
	c.InputCostPerToken = tkApplyBaseTaxMultiplier(c.InputCostPerToken)
	c.InputCostPerTokenPriority = tkApplyBaseTaxMultiplier(c.InputCostPerTokenPriority)
	c.OutputCostPerToken = tkApplyBaseTaxMultiplier(c.OutputCostPerToken)
	c.OutputCostPerTokenPriority = tkApplyBaseTaxMultiplier(c.OutputCostPerTokenPriority)
	c.ThinkingOutputCostPerToken = tkApplyBaseTaxMultiplier(c.ThinkingOutputCostPerToken)
	c.CacheCreationInputTokenCost = tkApplyBaseTaxMultiplier(c.CacheCreationInputTokenCost)
	c.CacheCreationInputTokenCostAbove1hr = tkApplyBaseTaxMultiplier(c.CacheCreationInputTokenCostAbove1hr)
	c.CacheReadInputTokenCost = tkApplyBaseTaxMultiplier(c.CacheReadInputTokenCost)
	c.CacheReadInputTokenCostPriority = tkApplyBaseTaxMultiplier(c.CacheReadInputTokenCostPriority)
	c.OutputCostPerImage = tkApplyBaseTaxMultiplier(c.OutputCostPerImage)
	c.OutputCostPerImageToken = tkApplyBaseTaxMultiplier(c.OutputCostPerImageToken)
	c.OutputCostPerSecond = tkApplyBaseTaxMultiplier(c.OutputCostPerSecond)
	if len(c.Intervals) > 0 {
		c.Intervals = tkApplyBaseTaxToPricingIntervals(c.Intervals)
	}
	return &c
}

func tkPresentLiteLLMModelPricing(p *LiteLLMModelPricing) *LiteLLMModelPricing {
	return tkApplyBaseTaxToLiteLLMModelPricingClone(p)
}

func tkApplyBaseTaxToModelPricingClone(p *ModelPricing) *ModelPricing {
	if p == nil {
		return nil
	}
	c := *p
	c.InputPricePerToken = tkApplyBaseTaxMultiplier(c.InputPricePerToken)
	c.InputPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.InputPricePerTokenPriority)
	c.ImageInputPricePerToken = tkApplyBaseTaxMultiplier(c.ImageInputPricePerToken)
	c.OutputPricePerToken = tkApplyBaseTaxMultiplier(c.OutputPricePerToken)
	c.OutputPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.OutputPricePerTokenPriority)
	c.ThinkingOutputPricePerToken = tkApplyBaseTaxMultiplier(c.ThinkingOutputPricePerToken)
	c.CacheCreationPricePerToken = tkApplyBaseTaxMultiplier(c.CacheCreationPricePerToken)
	c.CacheReadPricePerToken = tkApplyBaseTaxMultiplier(c.CacheReadPricePerToken)
	c.CacheReadPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.CacheReadPricePerTokenPriority)
	c.CacheCreation5mPrice = tkApplyBaseTaxMultiplier(c.CacheCreation5mPrice)
	c.CacheCreation1hPrice = tkApplyBaseTaxMultiplier(c.CacheCreation1hPrice)
	c.ImageOutputPricePerToken = tkApplyBaseTaxMultiplier(c.ImageOutputPricePerToken)
	if len(c.Intervals) > 0 {
		c.Intervals = tkApplyBaseTaxToPricingIntervals(c.Intervals)
	}
	return &c
}

func tkApplyBaseTaxToPublicCatalogPricing(vendor string, p *PublicCatalogPricing) {
	if p == nil || !tkLitellmProviderHasBaseTax(vendor) {
		return
	}
	p.InputPer1KTokens = tkApplyBaseTaxMultiplier(p.InputPer1KTokens)
	p.OutputPer1KTokens = tkApplyBaseTaxMultiplier(p.OutputPer1KTokens)
	p.ThinkingOutputPer1KTokens = tkApplyBaseTaxMultiplier(p.ThinkingOutputPer1KTokens)
	p.CacheReadPer1K = tkApplyBaseTaxMultiplier(p.CacheReadPer1K)
	p.CacheWritePer1K = tkApplyBaseTaxMultiplier(p.CacheWritePer1K)
	p.OutputCostPerImage = tkApplyBaseTaxMultiplier(p.OutputCostPerImage)
	p.OutputCostPerSecond = tkApplyBaseTaxMultiplier(p.OutputCostPerSecond)
	if len(p.Tiers) > 0 {
		for i := range p.Tiers {
			p.Tiers[i].InputPer1KTokens = tkApplyBaseTaxMultiplier(p.Tiers[i].InputPer1KTokens)
			p.Tiers[i].OutputPer1KTokens = tkApplyBaseTaxMultiplier(p.Tiers[i].OutputPer1KTokens)
			p.Tiers[i].CacheReadPer1K = tkApplyBaseTaxMultiplier(p.Tiers[i].CacheReadPer1K)
		}
	}
}

func tkApplyOfficialListBaseTaxForModel(model string, pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	if !tkLitellmProviderHasBaseTax(tkInferBaseTaxProvider(model)) {
		return pricing
	}
	return tkApplyBaseTaxToModelPricingClone(pricing)
}
