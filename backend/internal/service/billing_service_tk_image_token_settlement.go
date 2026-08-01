package service

import "strings"

// TK: image-token-priced registry owners must still settle on the IMAGE surface.
//
// Two settlement shapes exist among image_generation registry owners:
//
//   - per-image owners (imagen-*, seedream, grok-imagine-image, gemini-*-image):
//     `output_cost_per_image` (plus optional image_price_1k/2k/4k tiers). These
//     settle through CalculateImageCost, which multiplies a $/image unit by
//     ImageCount.
//   - per-image-TOKEN owners (OpenAI's gpt-image-* family):
//     `output_cost_per_image_token` and NO `output_cost_per_image`, because
//     OpenAI bills those models by generated image tokens, not by image.
//
// CalculateImageCost reads ONLY OutputCostPerImage, so a per-image-token owner
// reaching it charges $0 — while the unpriced-media guard still reports the model
// as priced (tkIsEffectivelyUnpriced sees the non-zero image-token price). That
// combination (bills $0 AND passes the gate) is exactly the hole
// docs/approved/priced-or-it-doesnt-ship.md exists to close, and it is why the
// settlement dimension must be derived from the owner row rather than assumed
// from the endpoint shape.
//
// The fix stays INSIDE the image funnels: both compute the image-token amount and
// keep BillingMode=image, so the usage log's billing_mode filter and the image
// rate multiplier are unchanged (this is a billing-correctness fix, not a
// reclassification). Scoped overrides still win — group size prices and
// channel_model_pricing are consulted before this, unchanged.

// TkImageModelBillsByImageTokens reports whether `model`'s registry owner is
// priced by image TOKEN and therefore must not be settled per-image. True only
// when the owner carries a positive per-image-token price and no per-image price
// at any tier — i.e. exactly the rows CalculateImageCost cannot charge for.
//
// Reads the resolved owner through PricingService when wired (so alias
// resolution matches billing) and falls back to the embedded registry snapshot
// otherwise, mirroring getDefaultImagePrice.
func (s *BillingService) TkImageModelBillsByImageTokens(model string) bool {
	if strings.TrimSpace(model) == "" {
		return false
	}
	pricing := tkOverlayLiteLLMModelPricing(model)
	if s != nil && s.pricingService != nil {
		pricing = s.pricingService.GetModelPricing(model)
	}
	return tkRegistryRowBillsImageByTokens(pricing)
}

// tkRegistryRowBillsImageByTokens is the pure predicate over a resolved registry
// row, kept separate so the settlement routers, the guard, and tests share one
// definition without needing a service graph.
func tkRegistryRowBillsImageByTokens(p *LiteLLMModelPricing) bool {
	if p == nil {
		return false
	}
	if p.OutputCostPerImageToken <= 0 {
		return false
	}
	return p.OutputCostPerImage <= 0 && p.ImagePrice1K <= 0 && p.ImagePrice2K <= 0 && p.ImagePrice4K <= 0
}

// tkRegistryRowHasBillableImagePrice reports whether a resolved row carries a
// price in SOME dimension one of the image settlement paths can actually charge.
// Two families qualify:
//
//   - per-image (any tier)      -> CalculateImageCost
//   - per-image-token           -> token settlement via the routing above
//
// A row outside both is unbillable on the image surface no matter which
// router it reaches. The concrete case this rejects is a row priced ONLY
// per-second (a video owner pointed at an image endpoint): tkIsEffectivelyUnpriced
// sees a non-zero media cost and calls it priced, yet every image path reads zero.
// Plain token prices and input-image-token prices alone are also insufficient:
// TkCalculateImageTokenCost intentionally routes only rows with a positive
// output-image-token rate, while CalculateImageCost reads only per-image rates.
// The guard must match those actual settlement predicates rather than admitting a
// dimension that no image funnel will charge.
func tkRegistryRowHasBillableImagePrice(p *LiteLLMModelPricing) bool {
	if p == nil {
		return false
	}
	if p.ExplicitFree {
		return true
	}
	return p.OutputCostPerImage > 0 ||
		p.ImagePrice1K > 0 || p.ImagePrice2K > 0 || p.ImagePrice4K > 0 ||
		tkRegistryRowBillsImageByTokens(p)
}

// TkImageModelBillsByImageTokens exposes the predicate to the OpenAI gateway
// service, which owns its own settlement funnel but depends on BillingService
// for every pricing fact.
func (s *OpenAIGatewayService) TkImageModelBillsByImageTokens(model string) bool {
	if s == nil || s.billingService == nil {
		return false
	}
	return s.billingService.TkImageModelBillsByImageTokens(model)
}

// TkCalculateImageTokenCost settles a per-image-token owner (gpt-image-*) on the
// IMAGE surface. The amount comes from the owner's token dimensions, but the
// breakdown keeps BillingMode=image so the usage-log billing_mode contract and the
// image rate multiplier behave exactly as before this fix.
//
// The arithmetic is deliberately NOT reimplemented here: it delegates to
// computeTokenBreakdown, the same engine the token path uses. That engine already
// owns the dimension precedence these rows depend on — image-input tokens billed at
// input_cost_per_image_token (falling back to the text input rate), generated image
// tokens billed at output_cost_per_image_token, and cache reads priced separately.
// Duplicating any of it here would recreate exactly the kind of second pricing
// implementation this PR exists to remove.
//
// Returns nil (caller falls through to per-image settlement) when the model is not
// a per-image-token owner, or when no generated image tokens were reported. In the
// latter case a model with no per-image price then legitimately reaches the
// unpriced-media guard rather than being charged a fabricated amount.
func (s *BillingService) TkCalculateImageTokenCost(
	model string,
	tokens UsageTokens,
	rateMultiplier float64,
) *CostBreakdown {
	if s == nil || tokens.ImageOutputTokens <= 0 {
		return nil
	}
	registryRow := tkOverlayLiteLLMModelPricing(model)
	if s.pricingService != nil {
		registryRow = s.pricingService.GetModelPricing(model)
	}
	if !tkRegistryRowBillsImageByTokens(registryRow) {
		return nil
	}
	// Resolve through the ordinary pricing chain so base tax and any model policy
	// apply identically to the token path.
	pricing, err := s.GetModelPricing(model)
	if err != nil || pricing == nil {
		return nil
	}
	// computeTokenBreakdown treats OutputTokens as the total that ImageOutputTokens
	// is carved out of. Image responses report the generated-image count on its own,
	// so ensure the total covers it rather than yielding a negative text remainder.
	if tokens.OutputTokens < tokens.ImageOutputTokens {
		tokens.OutputTokens = tokens.ImageOutputTokens
	}
	bd := s.computeTokenBreakdown(pricing, tokens, rateMultiplier, "", false, false)
	if bd == nil || bd.TotalCost <= 0 {
		return nil
	}
	// Deliberately image: this is the image surface billing an image-token-priced
	// owner, not a reclassification to token billing.
	bd.BillingMode = string(BillingModeImage)
	return bd
}

// Known limitation — the pre-flight balance HOLD still reserves $0 for these
// owners. EstimateImageHold routes through CalculateImageCost, which reads only
// OutputCostPerImage, so a per-image-token owner reserves nothing at submit time
// even though settlement now charges correctly.
//
// This is deliberately left alone rather than patched with an invented number: an
// upper-bound hold would need "image tokens per generated image", which no
// registry owner declares, and hardcoding one in Go would create exactly the
// second pricing source this package is converging away from. The gap is also not
// a regression — before this fix hold AND settlement were both $0; now only the
// hold is, so the worst case narrowed from "served free" to "served, then charged
// correctly, without a prior reservation". Closing it properly means adding an
// explicit tokens-per-image upper bound to the registry schema.
