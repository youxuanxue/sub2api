package service

import "strings"

func (s *BillingService) tkRegistryMediaPricing(model string) *LiteLLMModelPricing {
	if s != nil && s.pricingService != nil {
		return s.pricingService.GetModelPricing(model)
	}
	snapshot := loadTKPricingOverlaySnapshot()
	if snapshot == nil {
		return nil
	}
	owner := snapshot.Models[strings.ToLower(strings.TrimSpace(model))]
	return tkPresentLiteLLMModelPricingFromSnapshot(owner, snapshot)
}

// tkRegistryImageTierPrice resolves explicit size-tier prices from the active
// registry. Scoped group/channel prices are handled by callers before this.
func (s *BillingService) tkRegistryImageTierPrice(model, imageSize string) (float64, bool) {
	pricing := s.tkRegistryMediaPricing(model)
	if pricing == nil {
		return 0, false
	}
	var price float64
	switch NormalizeImageBillingTierOrDefault(imageSize) {
	case ImageBillingSize1K:
		price = pricing.ImagePrice1K
	case ImageBillingSize2K:
		price = pricing.ImagePrice2K
	case ImageBillingSize4K:
		price = pricing.ImagePrice4K
	}
	if price <= 0 {
		return 0, false
	}
	return price, true
}
