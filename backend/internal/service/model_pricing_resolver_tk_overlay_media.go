package service

// model_pricing_resolver_tk_overlay_media.go — image/video-only overlay → per-request resolver.
//
// BillingService.GetModelPricing intentionally fail-closes on TokenPricingAbsent
// entries (imagen-*/veo-* overlay rows) so token traffic cannot silently bill $0.
// When imagen traffic lands on the OpenAI token settlement path anyway (missing
// ImageCount from a non-/images/generations shape, or bridge accounting gap),
// ModelPricingResolver must still resolve a billable price from PricingService's
// OutputCostPerImage instead of returning an empty token BasePricing → $0
// served_zero_cost.

// tkResolveOverlayMediaPerRequest returns per-request/image resolved pricing when
// the model is priced only via OutputCostPerImage (TokenPricingAbsent) in
// PricingService. nil when not applicable.
func (r *ModelPricingResolver) tkResolveOverlayMediaPerRequest(model string) *ResolvedPricing {
	if r == nil || r.billingService == nil || r.billingService.pricingService == nil {
		return nil
	}
	lp := r.billingService.pricingService.GetModelPricing(model)
	if lp == nil || !lp.TokenPricingAbsent || lp.OutputCostPerImage <= 0 {
		return nil
	}
	mode := BillingModePerRequest
	if lp.Mode == "image_generation" {
		mode = BillingModeImage
	}
	return &ResolvedPricing{
		Mode:                   mode,
		DefaultPerRequestPrice: lp.OutputCostPerImage,
		Source:                 PricingSourceRegistry,
	}
}
