package service

import (
	"fmt"
	"strings"
)

// TK: media unpriced = reject (操作员拍板 2026-06-12，反转媒体路径的
// "先服务后补价"默认).
//
// Chat keeps serve-and-alert (a chat request is cents and availability wins;
// the served_zero_cost P0 probe surfaces the gap). Media is the opposite
// regime: one video task is up to ~$22 of upstream spend and images burn
// hard per-project provider quota — serving them unpriced (video bills $0,
// image falls back to a blind hardcoded $0.134) converts a pricing gap into
// real money loss before any operator can react to the P0. So the image and
// video gateway surfaces refuse to serve a model with no usable price: the
// pre-spend 400 below replaces the post-spend alert.
//
// This is also what makes new upstream channels safe to auto-enable: their
// models arrive unpriced → rejected → the human pricing act (overlay entry
// with per-entry source + failure_billing declaration, see
// scripts/checks/pricing-overlay.py) is the approval gate.
//
// Both predicates key off the REQUESTED model — the billing key — via the
// same PricingService.GetModelPricing the billing path resolves through, so
// guard and bill cannot drift. They fail OPEN on missing wiring (nil
// services: can't tell → don't block) and CLOSED on missing price.

// TkVideoModelUnpriced reports whether the requested model has no per-second
// video price. Video billing uses OutputCostPerSecond exclusively
// (CalculateVideoCost), so any model without it would bill $0 per second —
// including text models pointed at the video endpoint.
func (s *BillingService) TkVideoModelUnpriced(model string) bool {
	if s == nil || s.pricingService == nil {
		return false
	}
	pricing := s.pricingService.GetModelPricing(model)
	return pricing == nil || pricing.OutputCostPerSecond <= 0
}

// TkImageModelUnpriced reports whether the requested model has no usable
// image price from ANY static source: group-level size prices, a per-image
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
	if group != nil && (group.ImagePrice1K != nil || group.ImagePrice2K != nil || group.ImagePrice4K != nil) {
		return false
	}
	if s == nil || s.pricingService == nil {
		return false
	}
	pricing := s.pricingService.GetModelPricing(model)
	return pricing == nil || tkIsEffectivelyUnpriced(pricing)
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

// TkUnpricedMediaModelMessage is the client-facing 400 body for both media
// surfaces — explicit about WHY (no silent wrong charge) and about the way
// out (operator adds pricing).
func TkUnpricedMediaModelMessage(model, kind string) string {
	return fmt.Sprintf(
		"Model %q has no %s generation price configured on this gateway; unpriced media is not served (a silent zero or fallback charge is worse than a clear error). Ask the operator to add pricing for it first.",
		model, kind)
}
