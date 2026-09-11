package service

import (
	"fmt"
	"strings"
)

// TK: media unpriced = reject (操作员拍板 2026-06-12，反转媒体路径的
// "先服务后补价"默认).
//
// Image and video gateway surfaces reject missing prices before upstream spend.
// Generation price admission is owned by gateway_priced_serving_gate_tk.go.
//
// This is also what makes new upstream channels safe to auto-enable: their
// models arrive unpriced → rejected → the human pricing act (overlay entry
// with per-entry source + failure_billing declaration, see
// scripts/checks/pricing-overlay.py) is the approval gate.
//
// Both predicates key off the REQUESTED model — the billing key — via the
// billing price owners, including ModelPricingResolver for group image cards.
// They fail OPEN on missing wiring (nil
// services: can't tell → don't block) and CLOSED on missing price.

// TkVideoModelUnpriced reports whether the requested model has no per-second
// video price. Video billing uses OutputCostPerSecond exclusively
// (CalculateVideoCost), so any model without it would bill $0 per second —
// including text models pointed at the video endpoint.
func (s *BillingService) TkVideoModelUnpriced(model string) bool {
	if s == nil || s.pricingService == nil {
		return false
	}
	if min, ok := tkVideoMinUnitPriceUSD(model); ok && min > 0 {
		return false
	}
	pricing := s.pricingService.GetModelPricing(model)
	return pricing == nil || pricing.OutputCostPerSecond <= 0
}

// TkImageModelUnpriced reports whether the requested model has no usable
// image price from a group model card, group-level size prices, a per-image
// price, or token prices (gpt-image-style models bill by image tokens).
// Only the truly priceless are rejected — tkIsEffectivelyUnpriced treats
// litellm's all-zero placeholder rows as unpriced too. Channel-level DB
// pricing is deliberately not consulted (unknown before scheduling; per
// operating discipline it only holds non-zero corrections of models that
// already carry a static price, so it cannot be a model's sole price).
func (s *BillingService) TkImageModelUnpriced(model string, group *Group) bool {
	if strings.TrimSpace(model) == "" {
		// Model-less image requests are legal on the OAuth path (the forward
		// layer defaults them, e.g. to gpt-image-2) — defaulting and model
		// validation belong to that layer, so an empty name fails OPEN here.
		return false
	}
	if s != nil {
		resolved := NewModelPricingResolver(nil, s).resolveGroupPricing(PricingInput{Model: model, Group: group})
		if resolved != nil && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest) {
			// Settlement consumes a matching group image card even when empty
			// or nonpositive; a lower-priority registry price cannot admit it.
			return !tkResolvedImagePricingChargeable(resolved)
		}
	}
	if group != nil && (group.ImagePrice1K != nil || group.ImagePrice2K != nil || group.ImagePrice4K != nil) {
		return false
	}
	if s == nil || s.pricingService == nil {
		return false
	}
	pricing := s.pricingService.GetModelPricing(model)
	return !tkRegistryRowHasBillableImagePrice(pricing)
}

// Group image admission requires image/per-request prices. A token card alone
// cannot price count-only image usage; token-image support keeps its existing
// registry owner. Token-only interval fields are not per-request image prices.
func tkResolvedImagePricingChargeable(resolved *ResolvedPricing) bool {
	if resolved == nil {
		return false
	}
	switch resolved.Mode {
	case BillingModeImage, BillingModePerRequest:
		if resolved.DefaultPerRequestPrice > 0 {
			return true
		}
		for _, tier := range resolved.RequestTiers {
			if tier.PerRequestPrice != nil && *tier.PerRequestPrice > 0 {
				return true
			}
		}
	}
	return false
}

// TkVideoModelUnpriced / TkImageModelUnpriced — handler-facing wrappers so the
// gateway handlers depend on OpenAIGatewayService only.
func (s *OpenAIGatewayService) TkVideoModelUnpriced(model string) bool {
	if s == nil {
		return false
	}
	return s.billingService.TkVideoModelUnpriced(model)
}

func (s *OpenAIGatewayService) TkImageModelUnpriced(model string, group *Group) bool {
	if s == nil {
		return false
	}
	return s.billingService.TkImageModelUnpriced(model, group)
}

// TkTTSModelUnpriced reports whether the requested model has no character-priced
// TTS rate and no group audio_tts_price_per_million_chars override.
func (s *BillingService) TkTTSModelUnpriced(model string, group *Group) bool {
	if strings.TrimSpace(model) == "" {
		return false
	}
	if group != nil && group.AudioTTSPricePerMillionChars != nil {
		return false
	}
	if s == nil {
		return false
	}
	return s.TkRegistryTTSPricePerMillionChars(model) <= 0
}

func (s *OpenAIGatewayService) TkTTSModelUnpriced(model string, group *Group) bool {
	if s == nil {
		return false
	}
	return s.billingService.TkTTSModelUnpriced(model, group)
}

func (s *OpenAIGatewayService) TkSTTModelUnpriced(model string, group *Group) bool {
	if group != nil && group.AudioSTTPricePerHour != nil {
		return false
	}
	return s == nil || s.billingService == nil || s.billingService.TkRegistrySTTPricePerHour(model) <= 0
}

// TkUnpricedMediaModelMessage is the client-facing 400 body for both media
// surfaces — explicit about WHY (no silent wrong charge) and about the way
// out (operator adds pricing).
func TkUnpricedMediaModelMessage(model, kind string) string {
	return fmt.Sprintf(
		"Model %q has no %s generation price configured on this gateway; unpriced media is not served (a silent zero or fallback charge is worse than a clear error). Ask the operator to add pricing for it first.",
		model, kind)
}
