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

// getRegistryAliasPricing reuses the mature compatibility matcher without
// allowing its legacy numeric table to participate in billing. The matcher
// identifies a family; the returned dimensions are always re-read from the
// active registry owner so hot price changes take effect immediately.
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

	// This compatibility SKU routes to ordinary GPT-5.5. A provider-advertised
	// Pro row is sensor evidence only and is not a serving/billing owner.
	if normalizeOpenAIBillingModel(lower) == "gpt-5.5-pro" {
		return tkRegistryAliasOwnerPricing("gpt-5.5")
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
		"gemini-3.1-pro", "gemini-3.6-flash", "gemini-2.5-pro",
		"gemini-2.5-flash-lite", "gemini-2.5-flash", "glm-4.7-flash",
		"glm-4.5-flash", "kimi-k3", "kimi-k2.6", "kimi-k2.5", "kimi-k2-thinking", "kimi-k2",
		"minimax-m3", "minimax-m2.7-highspeed", "minimax-m2.7",
		"minimax-m2.5", "minimax-m2.1", "minimax-m2",
		"doubao-embedding-vision", "gpt-5.4", "gpt-5.2", "gpt-5.3-codex",
		"grok-4.5", "grok-4.3", "grok-build-0.1",
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
