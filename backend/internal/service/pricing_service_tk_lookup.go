package service

import (
	"strings"
)

// tkGetModelPricing is the TokenKey lookup implementation behind GetModelPricing.
func (s *PricingService) tkGetModelPricing(modelName string) *LiteLLMModelPricing {
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
	lookupCandidates := s.buildModelLookupCandidates(modelLower)

	// 1. 精确匹配
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := pricingData[candidate]; ok {
			return present(pricing)
		}
	}

	// 1b. Declared public aliases (_aliases) — sole owner for public id → price
	// card folds such as gpt-5.6 / gpt-5.6-chat-latest → gpt-5.6-sol. Must run
	// before fuzzy / OpenAI family fallback, or an alias without a model row
	// silently inherits the gpt-5.4 default card (SSOT).
	if snapshot != nil {
		for _, candidate := range lookupCandidates {
			if candidate == "" {
				continue
			}
			owner, ok := snapshot.Aliases[candidate]
			if !ok || owner == "" {
				continue
			}
			if pricing, ok := pricingData[owner]; ok {
				return present(pricing)
			}
		}
	}

	// 2. 处理常见的模型名称变体
	// claude-opus-4-5-20251101 -> claude-opus-4.5-20251101
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := pricingData[normalized]; ok {
			return present(pricing)
		}
	}

	// 3. 尝试模糊匹配（去掉版本号后缀）
	// claude-opus-4-5-20251101 -> claude-opus-4.5
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return present(pricing)
		}
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

// tkGetIdentifiedModelPricing is the TokenKey lookup behind GetIdentifiedModelPricing.
func (s *PricingService) tkGetIdentifiedModelPricing(modelName string) *LiteLLMModelPricing {
	if s == nil || strings.TrimSpace(modelName) == "" {
		return nil
	}
	var pricingData map[string]*LiteLLMModelPricing
	var present func(*LiteLLMModelPricing) *LiteLLMModelPricing
	if s.useActiveRegistry {
		snapshot := loadTKPricingOverlaySnapshot()
		if snapshot != nil {
			pricingData = snapshot.Models
			present = func(pricing *LiteLLMModelPricing) *LiteLLMModelPricing {
				return tkPresentLiteLLMModelPricingFromSnapshot(pricing, snapshot)
			}
		}
	} else {
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
	modelLower := strings.ToLower(strings.TrimSpace(modelName))
	lookupCandidates := s.buildModelLookupCandidates(modelLower)
	for _, candidate := range lookupCandidates {
		if candidate == "" {
			continue
		}
		if pricing, ok := pricingData[candidate]; ok {
			return present(pricing)
		}
	}
	for _, candidate := range lookupCandidates {
		normalized := strings.ReplaceAll(candidate, "-4-5-", "-4.5-")
		if pricing, ok := pricingData[normalized]; ok {
			return present(pricing)
		}
	}
	baseName := s.extractBaseName(lookupCandidates[0])
	for key, pricing := range pricingData {
		keyBase := s.extractBaseName(strings.ToLower(key))
		if keyBase == baseName {
			return present(pricing)
		}
	}
	return nil
}

func (s *PricingService) buildModelLookupCandidates(modelLower string) []string {
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
