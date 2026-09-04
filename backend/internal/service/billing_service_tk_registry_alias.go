package service

import "strings"

// tkRegistryAliasOwnerPricing presents one owner and the executable pricing
// policy from the same immutable registry snapshot. This prevents a hot reload
// from pairing an old alias price with a new tax policy, or vice versa.
func tkRegistryAliasOwnerPricing(owner string) *ModelPricing {
	owner = strings.ToLower(strings.TrimSpace(owner))
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return nil
	}
	pricing := tkModelPricingFromLiteLLM(
		tkPresentLiteLLMModelPricingFromSnapshot(snapshot.Models[owner], snapshot),
	)
	if pricing != nil {
		pricing.registryOwner = owner
	}
	return pricing
}

func tkModelPricingFromLiteLLM(p *LiteLLMModelPricing) *ModelPricing {
	if p == nil || p.TokenPricingAbsent || tkIsEffectivelyUnpriced(p) {
		return nil
	}
	price5m := p.CacheCreationInputTokenCost
	price1h := p.CacheCreationInputTokenCostAbove1hr
	return &ModelPricing{
		InputPricePerToken:                 p.InputCostPerToken,
		InputPricePerTokenPriority:         p.InputCostPerTokenPriority,
		OutputPricePerToken:                p.OutputCostPerToken,
		OutputPricePerTokenPriority:        p.OutputCostPerTokenPriority,
		ThinkingOutputPricePerToken:        p.ThinkingOutputCostPerToken,
		CacheCreationPricePerToken:         p.CacheCreationInputTokenCost,
		CacheCreationPricePerTokenPriority: p.CacheCreationInputTokenCostPriority,
		CacheReadPricePerToken:             p.CacheReadInputTokenCost,
		CacheReadPricePerTokenPriority:     p.CacheReadInputTokenCostPriority,
		CacheCreation5mPrice:               price5m,
		CacheCreation1hPrice:               price1h,
		SupportsCacheBreakdown:             price1h > 0 && price1h > price5m,
		LongContextInputThreshold:          p.LongContextInputTokenThreshold,
		LongContextThresholdInclusive:      p.LongContextThresholdInclusive,
		LongContextInputMultiplier:         p.LongContextInputCostMultiplier,
		LongContextOutputMultiplier:        p.LongContextOutputCostMultiplier,
		ImageInputPricePerToken:            p.InputCostPerImageToken,
		ImageOutputPricePerToken:           p.OutputCostPerImageToken,
		Intervals:                          p.Intervals,
		registrySnapshot:                   p.registrySnapshot,
	}
}

func tkOverlayModelPricing(model string) *ModelPricing {
	owner := strings.ToLower(strings.TrimSpace(model))
	pricing := tkModelPricingFromLiteLLM(loadTKPricingOverlay()[owner])
	if pricing != nil {
		pricing.registryOwner = owner
	}
	return pricing
}

func tkPricingRegistryAliasOwner(model string) (string, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return "", false
	}
	owner, ok := snapshot.Aliases[model]
	return owner, ok
}

// getRegistryAliasPricing resolves settlement price after the public alias
// owner. Declared public aliases live only in overlay `_aliases`. What remains
// below is family-floor remapping so unknown IDs in a known family still bill
// from the live registry owner, never the legacy numeric table.
func (s *BillingService) getRegistryAliasPricing(model string) *ModelPricing {
	if s == nil {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	if !s.useActiveRegistryAliases {
		return s.getFallbackPricing(lower)
	}
	if direct := tkRegistryAliasOwnerPricing(lower); direct != nil {
		return direct
	}
	if owner, declared := tkPricingRegistryAliasOwner(lower); declared {
		return tkRegistryAliasOwnerPricing(owner)
	}

	// xAI: resolve known aliases via their routing canonical → pricing owner.
	// This sits before the family floor so direct registry rows (above) still
	// take precedence, and known aliases without a public pricing owner fail
	// closed rather than inheriting the unknown-model floor.
	if grokOwner, known := resolveGrokTextPricingOwner(lower); known {
		return tkRegistryAliasOwnerPricing(grokOwner)
	}

	legacy := s.getFallbackPricing(lower)
	if legacy == nil {
		return nil
	}
	if legacy.registryOwner != "" {
		return tkRegistryAliasOwnerPricing(legacy.registryOwner)
	}

	// Several historical map keys intentionally share a pointer. Resolve those
	// families by requested shape before the identity table below.
	if strings.Contains(lower, "opus") {
		switch {
		case strings.Contains(lower, "opus-5") || strings.Contains(lower, "opus5"):
			return tkRegistryAliasOwnerPricing("claude-opus-5")
		case strings.Contains(lower, "4.8") || strings.Contains(lower, "4-8"):
			return tkRegistryAliasOwnerPricing("claude-opus-4.8")
		case strings.Contains(lower, "4.7") || strings.Contains(lower, "4-7"):
			return tkRegistryAliasOwnerPricing("claude-opus-4.7")
		case strings.Contains(lower, "4.6") || strings.Contains(lower, "4-6"):
			return tkRegistryAliasOwnerPricing("claude-opus-4.6")
		case strings.Contains(lower, "4.5") || strings.Contains(lower, "4-5"):
			return tkRegistryAliasOwnerPricing("claude-opus-4.5")
		}
	}
	if normalized := normalizeOpenAIBillingModel(lower); normalized != "" {
		switch normalized {
		case "gpt-5.5":
			return tkRegistryAliasOwnerPricing("gpt-5.5")
		case "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
			"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.2":
			return tkRegistryAliasOwnerPricing(normalized)
		case "gpt-5.3-codex", "gpt-5.3-codex-spark":
			return tkRegistryAliasOwnerPricing("gpt-5.3-codex")
		}
	}

	owners := []string{
		"claude-sonnet-4", "claude-3-5-sonnet", "claude-3-5-haiku",
		"claude-3-opus", "claude-3-haiku", "claude-fable-5",
		"gemini-3.1-pro", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-2.5-pro",
		"gemini-2.5-flash-lite", "gemini-2.5-flash", "glm-4.7-flash",
		"glm-4.5-flash", "kimi-k3", "kimi-k2.6", "kimi-k2.5", "kimi-k2-thinking", "kimi-k2",
		"minimax-m3", "minimax-m2.7-highspeed", "minimax-m2.7",
		"minimax-m2.5", "minimax-m2.1", "minimax-m2",
		"doubao-embedding-vision", "gpt-5.4", "gpt-5.2", "gpt-5.3-codex",
		"grok-4.6", "grok-4.5", "grok-4.3", "grok-build-0.1",
	}
	for _, owner := range owners {
		if fallback := s.fallbackPrices[owner]; fallback != nil && fallback == legacy {
			return tkRegistryAliasOwnerPricing(owner)
		}
	}

	// A newly added legacy numeric matcher is not a price owner. It must be mapped
	// explicitly above before constructor-created billing services may use it.
	return nil
}
