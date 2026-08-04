package service

import (
	"errors"
	"fmt"
	"strings"
)

var ErrImageUsageTokensUnavailable = errors.New("image output tokens unavailable for token-priced image owner")

func tkRegistryRowBillsImageByTokens(p *LiteLLMModelPricing) bool {
	if p == nil || p.OutputCostPerImageToken <= 0 {
		return false
	}
	return p.OutputCostPerImage <= 0 && p.ImagePrice1K <= 0 &&
		p.ImagePrice2K <= 0 && p.ImagePrice4K <= 0
}

func tkRegistryRowHasBillableImagePrice(p *LiteLLMModelPricing) bool {
	if p == nil {
		return false
	}
	if p.ExplicitFree {
		return true
	}
	return p.OutputCostPerImage > 0 || p.ImagePrice1K > 0 ||
		p.ImagePrice2K > 0 || p.ImagePrice4K > 0 ||
		p.OutputCostPerImageToken > 0
}

func (s *BillingService) TkImageModelBillsByImageTokens(model string) bool {
	if s == nil || strings.TrimSpace(model) == "" {
		return false
	}
	var pricing *LiteLLMModelPricing
	if s.pricingService != nil {
		pricing = s.pricingService.GetModelPricing(model)
	} else {
		pricing = loadTKPricingOverlay()[strings.ToLower(strings.TrimSpace(model))]
	}
	return tkRegistryRowBillsImageByTokens(pricing)
}

// TkCalculateImageTokenCost delegates arithmetic to the shared token engine but
// keeps BillingMode=image. A token-priced owner without reported output tokens
// fails closed instead of falling through to a zero per-image charge.
func (s *BillingService) TkCalculateImageTokenCost(model string, tokens UsageTokens, rateMultiplier float64) (*CostBreakdown, error) {
	if s == nil || !s.TkImageModelBillsByImageTokens(model) {
		return nil, nil
	}
	if tokens.ImageOutputTokens <= 0 {
		return nil, fmt.Errorf("%w: model=%s", ErrImageUsageTokensUnavailable, model)
	}
	pricing, err := s.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil, fmt.Errorf("resolve image-token owner %s: %w", model, err)
	}
	if tokens.OutputTokens < tokens.ImageOutputTokens {
		tokens.OutputTokens = tokens.ImageOutputTokens
	}
	breakdown := s.computeTokenBreakdown(pricing, tokens, rateMultiplier, "", false, false)
	if breakdown == nil || breakdown.TotalCost <= 0 {
		return nil, fmt.Errorf("image-token owner %s produced a non-positive settlement", model)
	}
	breakdown.BillingMode = string(BillingModeImage)
	return breakdown, nil
}
