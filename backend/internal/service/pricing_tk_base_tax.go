package service

import (
	"fmt"
	"math"
	"strings"
)

type tkOfficialListBaseTaxRule struct {
	Provider      string   `json:"provider"`
	ModelPrefixes []string `json:"model_prefixes"`
	ModelContains []string `json:"model_contains"`
}

// tkOfficialListBaseTaxPolicy is executable pricing policy loaded from
// tk_pricing_overlay.json::_config. The embedded document is the compile floor;
// tk_pricing_overlay_runtime may replace this policy atomically with model prices.
type tkOfficialListBaseTaxPolicy struct {
	Multiplier float64                     `json:"multiplier"`
	Rules      []tkOfficialListBaseTaxRule `json:"rules"`
}

func (p tkOfficialListBaseTaxPolicy) validate() error {
	if math.IsNaN(p.Multiplier) || math.IsInf(p.Multiplier, 0) || p.Multiplier < 1 || p.Multiplier > 2 {
		return fmt.Errorf("official_list_base_tax.multiplier must be within [1,2], got %v", p.Multiplier)
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("official_list_base_tax.rules must be non-empty")
	}
	providers := make(map[string]struct{}, len(p.Rules))
	matchers := make(map[string]string)
	for i, rule := range p.Rules {
		provider := strings.TrimSpace(rule.Provider)
		if provider == "" || provider != strings.ToLower(provider) {
			return fmt.Errorf("official_list_base_tax.rules[%d].provider must be normalized lowercase", i)
		}
		if _, exists := providers[provider]; exists {
			return fmt.Errorf("official_list_base_tax provider %q is duplicated", provider)
		}
		providers[provider] = struct{}{}
		if len(rule.ModelPrefixes) == 0 && len(rule.ModelContains) == 0 {
			return fmt.Errorf("official_list_base_tax provider %q requires a fallback model matcher", provider)
		}
		for _, matcherSet := range []struct {
			kind   string
			values []string
		}{
			{kind: "prefix", values: rule.ModelPrefixes},
			{kind: "contains", values: rule.ModelContains},
		} {
			kind, values := matcherSet.kind, matcherSet.values
			seen := make(map[string]struct{}, len(values))
			for _, value := range values {
				if value == "" || value != strings.TrimSpace(value) || value != strings.ToLower(value) {
					return fmt.Errorf("official_list_base_tax provider %q has invalid %s matcher %q", provider, kind, value)
				}
				if _, exists := seen[value]; exists {
					return fmt.Errorf("official_list_base_tax provider %q duplicates %s matcher %q", provider, kind, value)
				}
				seen[value] = struct{}{}
				key := kind + ":" + value
				if owner, exists := matchers[key]; exists {
					return fmt.Errorf("official_list_base_tax %s matcher %q belongs to both %q and %q", kind, value, owner, provider)
				}
				matchers[key] = provider
			}
		}
	}
	return nil
}

func loadTkOfficialListBaseTaxPolicy() tkOfficialListBaseTaxPolicy {
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return tkOfficialListBaseTaxPolicy{}
	}
	return snapshot.BaseTax
}

func tkOfficialListBaseTaxMultiplier() float64 {
	return loadTkOfficialListBaseTaxPolicy().Multiplier
}

func (p tkOfficialListBaseTaxPolicy) multiplierForProvider(provider string) (float64, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for _, rule := range p.Rules {
		if provider == rule.Provider {
			return p.Multiplier, true
		}
	}
	return 0, false
}

func tkBaseTaxMultiplierForProvider(provider string) (float64, bool) {
	return loadTkOfficialListBaseTaxPolicy().multiplierForProvider(provider)
}

// tkInferBaseTaxProvider maps a bare model id to a provider when only the model
// name is known (billing fallbackPrices path — those entries carry no vendor).
func (p tkOfficialListBaseTaxPolicy) inferProvider(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	for _, rule := range p.Rules {
		for _, prefix := range rule.ModelPrefixes {
			if strings.HasPrefix(m, prefix) {
				return rule.Provider
			}
		}
		for _, fragment := range rule.ModelContains {
			if strings.Contains(m, fragment) {
				return rule.Provider
			}
		}
	}
	return ""
}

func tkInferBaseTaxProvider(model string) string {
	return loadTkOfficialListBaseTaxPolicy().inferProvider(model)
}

func tkApplyBaseTaxMultiplier(v, multiplier float64) float64 {
	if v <= 0 {
		return v
	}
	return v * multiplier
}

func tkApplyBaseTaxToPricingInterval(iv PricingInterval, multiplier float64) PricingInterval {
	out := iv
	if out.InputPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.InputPrice, multiplier)
		out.InputPrice = &v
	}
	if out.OutputPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.OutputPrice, multiplier)
		out.OutputPrice = &v
	}
	if out.CacheWritePrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.CacheWritePrice, multiplier)
		out.CacheWritePrice = &v
	}
	if out.CacheReadPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.CacheReadPrice, multiplier)
		out.CacheReadPrice = &v
	}
	if out.PerRequestPrice != nil {
		v := tkApplyBaseTaxMultiplier(*out.PerRequestPrice, multiplier)
		out.PerRequestPrice = &v
	}
	return out
}

func tkApplyBaseTaxToPricingIntervals(intervals []PricingInterval, multiplier float64) []PricingInterval {
	if len(intervals) == 0 {
		return intervals
	}
	out := make([]PricingInterval, len(intervals))
	for i := range intervals {
		out[i] = tkApplyBaseTaxToPricingInterval(intervals[i], multiplier)
	}
	return out
}

func tkApplyBaseTaxToLiteLLMModelPricingClone(p *LiteLLMModelPricing) *LiteLLMModelPricing {
	if p == nil {
		return p
	}
	multiplier, ok := tkBaseTaxMultiplierForProvider(p.LiteLLMProvider)
	if !ok {
		return p
	}
	c := *p
	c.InputCostPerToken = tkApplyBaseTaxMultiplier(c.InputCostPerToken, multiplier)
	c.InputCostPerTokenPriority = tkApplyBaseTaxMultiplier(c.InputCostPerTokenPriority, multiplier)
	c.OutputCostPerToken = tkApplyBaseTaxMultiplier(c.OutputCostPerToken, multiplier)
	c.OutputCostPerTokenPriority = tkApplyBaseTaxMultiplier(c.OutputCostPerTokenPriority, multiplier)
	c.ThinkingOutputCostPerToken = tkApplyBaseTaxMultiplier(c.ThinkingOutputCostPerToken, multiplier)
	c.CacheCreationInputTokenCost = tkApplyBaseTaxMultiplier(c.CacheCreationInputTokenCost, multiplier)
	c.CacheCreationInputTokenCostPriority = tkApplyBaseTaxMultiplier(c.CacheCreationInputTokenCostPriority, multiplier)
	c.CacheCreationInputTokenCostAbove1hr = tkApplyBaseTaxMultiplier(c.CacheCreationInputTokenCostAbove1hr, multiplier)
	c.CacheReadInputTokenCost = tkApplyBaseTaxMultiplier(c.CacheReadInputTokenCost, multiplier)
	c.CacheReadInputTokenCostPriority = tkApplyBaseTaxMultiplier(c.CacheReadInputTokenCostPriority, multiplier)
	c.OutputCostPerImage = tkApplyBaseTaxMultiplier(c.OutputCostPerImage, multiplier)
	c.OutputCostPerImageToken = tkApplyBaseTaxMultiplier(c.OutputCostPerImageToken, multiplier)
	c.OutputCostPerSecond = tkApplyBaseTaxMultiplier(c.OutputCostPerSecond, multiplier)
	if len(c.Intervals) > 0 {
		c.Intervals = tkApplyBaseTaxToPricingIntervals(c.Intervals, multiplier)
	}
	return &c
}

func tkPresentLiteLLMModelPricing(p *LiteLLMModelPricing) *LiteLLMModelPricing {
	return tkApplyBaseTaxToLiteLLMModelPricingClone(p)
}

func tkApplyBaseTaxToModelPricingClone(p *ModelPricing, multiplier float64) *ModelPricing {
	if p == nil {
		return nil
	}
	c := *p
	c.InputPricePerToken = tkApplyBaseTaxMultiplier(c.InputPricePerToken, multiplier)
	c.InputPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.InputPricePerTokenPriority, multiplier)
	c.ImageInputPricePerToken = tkApplyBaseTaxMultiplier(c.ImageInputPricePerToken, multiplier)
	c.OutputPricePerToken = tkApplyBaseTaxMultiplier(c.OutputPricePerToken, multiplier)
	c.OutputPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.OutputPricePerTokenPriority, multiplier)
	c.ThinkingOutputPricePerToken = tkApplyBaseTaxMultiplier(c.ThinkingOutputPricePerToken, multiplier)
	c.CacheCreationPricePerToken = tkApplyBaseTaxMultiplier(c.CacheCreationPricePerToken, multiplier)
	c.CacheCreationPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.CacheCreationPricePerTokenPriority, multiplier)
	c.CacheReadPricePerToken = tkApplyBaseTaxMultiplier(c.CacheReadPricePerToken, multiplier)
	c.CacheReadPricePerTokenPriority = tkApplyBaseTaxMultiplier(c.CacheReadPricePerTokenPriority, multiplier)
	c.CacheCreation5mPrice = tkApplyBaseTaxMultiplier(c.CacheCreation5mPrice, multiplier)
	c.CacheCreation1hPrice = tkApplyBaseTaxMultiplier(c.CacheCreation1hPrice, multiplier)
	c.ImageOutputPricePerToken = tkApplyBaseTaxMultiplier(c.ImageOutputPricePerToken, multiplier)
	if len(c.Intervals) > 0 {
		c.Intervals = tkApplyBaseTaxToPricingIntervals(c.Intervals, multiplier)
	}
	return &c
}

func tkApplyBaseTaxToPublicCatalogPricing(vendor string, p *PublicCatalogPricing) {
	if p == nil {
		return
	}
	multiplier, ok := tkBaseTaxMultiplierForProvider(vendor)
	if !ok {
		return
	}
	p.InputPer1KTokens = tkApplyBaseTaxMultiplier(p.InputPer1KTokens, multiplier)
	p.OutputPer1KTokens = tkApplyBaseTaxMultiplier(p.OutputPer1KTokens, multiplier)
	p.ThinkingOutputPer1KTokens = tkApplyBaseTaxMultiplier(p.ThinkingOutputPer1KTokens, multiplier)
	p.CacheReadPer1K = tkApplyBaseTaxMultiplier(p.CacheReadPer1K, multiplier)
	p.CacheWritePer1K = tkApplyBaseTaxMultiplier(p.CacheWritePer1K, multiplier)
	p.OutputCostPerImage = tkApplyBaseTaxMultiplier(p.OutputCostPerImage, multiplier)
	p.OutputCostPerSecond = tkApplyBaseTaxMultiplier(p.OutputCostPerSecond, multiplier)
	if len(p.Tiers) > 0 {
		for i := range p.Tiers {
			p.Tiers[i].InputPer1KTokens = tkApplyBaseTaxMultiplier(p.Tiers[i].InputPer1KTokens, multiplier)
			p.Tiers[i].OutputPer1KTokens = tkApplyBaseTaxMultiplier(p.Tiers[i].OutputPer1KTokens, multiplier)
			p.Tiers[i].CacheReadPer1K = tkApplyBaseTaxMultiplier(p.Tiers[i].CacheReadPer1K, multiplier)
		}
	}
}

func tkApplyOfficialListBaseTaxForModel(model string, pricing *ModelPricing) *ModelPricing {
	if pricing == nil {
		return nil
	}
	policy := loadTkOfficialListBaseTaxPolicy()
	multiplier, ok := policy.multiplierForProvider(policy.inferProvider(model))
	if !ok {
		return pricing
	}
	return tkApplyBaseTaxToModelPricingClone(pricing, multiplier)
}
