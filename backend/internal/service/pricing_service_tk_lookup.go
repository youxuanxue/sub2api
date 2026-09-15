package service

import (
	"strings"
)

// tkGetModelPricing is the TokenKey lookup implementation behind GetModelPricing.
func (s *PricingService) tkGetModelPricing(modelName string) *LiteLLMModelPricing {
	return s.tkLookupModelPricing(modelName, false)
}

func (s *PricingService) tkGetIdentifiedModelPricing(modelName string) *LiteLLMModelPricing {
	return s.tkLookupModelPricing(modelName, true)
}

// Both callers resolve owners and aliases from the same immutable snapshot.
// Response-model billing stops before family floors because upstream declarations
// must identify a price explicitly rather than guess a family.
func (s *PricingService) tkLookupModelPricing(modelName string, identifiedOnly bool) *LiteLLMModelPricing {
	if modelName == "" {
		return nil
	}
	var snapshot *tkPricingOverlaySnapshot
	var pricingData map[string]*LiteLLMModelPricing
	var present func(*LiteLLMModelPricing) *LiteLLMModelPricing
	if s != nil && s.useActiveRegistry {
		snapshot = loadTKPricingOverlaySnapshot()
		if snapshot != nil {
			pricingData = snapshot.Models
			present = func(pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
				return tkPresentLiteLLMModelPricingFromSnapshot(pricing, snapshot)
			}
		}
	} else if s != nil {
		s.mu.RLock()
		pricingData = s.pricingData
		s.mu.RUnlock()
		baseTax := loadTkOfficialListBaseTaxPolicy()
		present = func(pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
			return tkApplyBaseTaxToLiteLLMModelPricingCloneWithPolicy(pricing, baseTax)
		}
	}
	if pricingData == nil || present == nil {
		return nil
	}
	lookupService := &PricingService{pricingData: pricingData}

	// 标准化模型名称（同时兼容 "models/xxx"、VertexAI 资源名等前缀）
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := buildModelLookupCandidates(modelLower)

	var aliases map[string]string
	if snapshot != nil {
		aliases = snapshot.Aliases
	}
	if pricing := lookupService.matchIdentifiedModelPricing(lookupCandidates, aliases); pricing != nil {
		return present(pricing)
	}
	if identifiedOnly {
		return nil
	}

	// 4. 基于模型系列匹配（Claude）
	if pricing := lookupService.matchByModelFamily(lookupCandidates[0]); pricing != nil {
		return present(pricing)
	}

	// 5. OpenAI 模型回退策略
	if strings.HasPrefix(lookupCandidates[0], "gpt-") {
		return present(lookupService.matchOpenAIModel(lookupCandidates[0]))
	}

	// 6. Provider-prefixed 最后兜底仅兼容直接构造 PricingService 的聚焦测试夹具。
	// 生产构造器读取只含 normalized bare owners 的 active registry，必在第 1 步 exact match。
	if pricing := lookupService.matchByProviderPrefix(lookupCandidates[0]); pricing != nil {
		return present(pricing)
	}

	return nil
}

// matchIdentifiedModelPricing owns exact, declared-alias and dated-name lookup.
// A bare owner wins over dated snapshots; if only dated rows exist, use the
// newest matching spelling deterministically. Never match a different variant
// merely because its key contains the requested family name.
func (s *PricingService) matchIdentifiedModelPricing(candidates []string, aliases map[string]string) *LiteLLMModelPricing {
	for _, candidate := range candidates {
		if pricing := s.pricingData[candidate]; pricing != nil {
			return pricing
		}
	}
	for _, candidate := range candidates {
		if owner, ok := aliases[candidate]; ok {
			if pricing := s.pricingData[owner]; pricing != nil {
				return pricing
			}
		}
	}
	for _, candidate := range candidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing := s.pricingData[normalized]; pricing != nil {
			return pricing
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	baseName := s.extractBaseName(candidates[0])
	if pricing := s.pricingData[baseName]; pricing != nil {
		return pricing
	}
	var selected string
	for key, pricing := range s.pricingData {
		if pricing != nil && s.extractBaseName(strings.ToLower(key)) == baseName && key > selected {
			selected = key
		}
	}
	return s.pricingData[selected]
}

// buildModelLookupCandidates owns exact price-key spelling for billing and catalog lookups.
func buildModelLookupCandidates(modelLower string) []string {
	rawCandidates := []string{
		modelLower,
		strings.TrimPrefix(modelLower, "models/"),
		lastSegment(modelLower),
		lastSegment(strings.TrimPrefix(modelLower, "models/")),
	}
	normalized := normalizeModelNameForPricing(modelLower)

	// A tier-specific entry should take precedence when the pricing catalog gains
	// one later. Today Antigravity's Gemini 3.6 Flash tiers share the base rate,
	// so the normalized base remains the fallback after the exact aliases.
	candidates := rawCandidates
	if normalizeGeminiThinkingTierAlias(lastSegment(modelLower)) != lastSegment(modelLower) {
		candidates = append(candidates, normalized)
	} else {
		// Prefer canonical model names for all other aliases (including models/xxx).
		candidates = append([]string{normalized}, candidates...)
	}

	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return []string{modelLower}
	}
	return out
}

// normalizeGeminiThinkingTierAlias maps Antigravity Gemini Flash thinking-tier
// model IDs to the public base model. The tier controls reasoning behavior, not
// the published token rate, so -high/-low/-medium/-tiered stay on the same
// price card as the public id.
func normalizeGeminiThinkingTierAlias(model string) string {
	for _, baseModel := range []string{"gemini-3.6-flash", "gemini-3.7-flash", "gemini-3.8-flash"} {
		for _, tier := range []string{"-high", "-low", "-medium", "-tiered"} {
			if model == baseModel+tier {
				return baseModel
			}
		}
	}
	return model
}
