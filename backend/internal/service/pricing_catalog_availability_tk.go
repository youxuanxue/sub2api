package service

import "context"

// tkAvailabilityStructurallyGone reports whether availability says the model
// does NOT EXIST upstream — `model_not_found` having flipped status to
// `unreachable`. This is the "gone" half of the gone-vs-degraded split (us7 P0
// 2026-06-13): a model the upstream rejects as not-found is structurally gone
// (it will not self-recover) and is hidden from the servable surfaces, whereas
// a model with TRANSIENT trouble (5xx / network / rate-limit → stale or
// soft-unreachable) keeps its badge and stays listed — so a normal model having
// a bad few minutes never flaps in and out of the storefront. model_not_found
// is platform-wide (the model exists or it doesn't), which matches
// model_availability's (platform, model) global keying; account-level signals
// (rate_limit / auth) never set this kind, so they cannot hide a model here.
func tkAvailabilityStructurallyGone(s AvailabilityState) bool {
	return s.Status == AvailabilityStatusUnreachable && s.LastFailureKind == FailureKindModelNotFound
}

// DecorateAndPruneByAvailability overlays per-model availability badges AND
// removes structurally-gone models (tkAvailabilityStructurallyGone) from the
// catalog response, in a single pass (one GetAvailability per model). It is the
// sole availability pass on the public /pricing path. This is the catalog
// self-heal: a model the upstream stops serving (e.g. an access-gated
// claude-fable-5 answering 404 model_not_found) auto-disappears from the public
// /pricing storefront without a manual servable-allowlist edit, while
// degraded-but-present models keep their badge.
//
// Mapping: /pricing models carry no platform dimension, so platform is inferred
// from the vendor/litellm_provider field; an unknown vendor yields no badge and
// is never pruned (passed through). svc == nil (Phase-1 flag off) → returns resp
// unchanged: no badges, no pruning.
func DecorateAndPruneByAvailability(ctx context.Context, resp *PublicCatalogResponse, svc *PricingAvailabilityService) *PublicCatalogResponse {
	if svc == nil || resp == nil || len(resp.Data) == 0 {
		return resp
	}
	out := &PublicCatalogResponse{
		Object:    resp.Object,
		UpdatedAt: resp.UpdatedAt,
		Data:      make([]PublicCatalogModel, 0, len(resp.Data)),
	}
	for _, model := range resp.Data {
		platform := inferPlatformFromVendor(model.Vendor)
		if platform == "" {
			out.Data = append(out.Data, model)
			continue
		}
		state, err := svc.GetAvailability(ctx, platform, model.ModelID)
		if err != nil {
			out.Data = append(out.Data, model)
			continue
		}
		if tkAvailabilityStructurallyGone(state) {
			continue // gone upstream → hide from the storefront
		}
		model.Availability = stateToAvailability(state)
		out.Data = append(out.Data, model)
	}
	return out
}

// inferPlatformFromVendor maps the vendor string in the catalog to the
// platform string used in model_availability. Vendor values come from the
// litellm_provider field in model_pricing.json.
func inferPlatformFromVendor(vendor string) string {
	switch vendor {
	case "gemini", "google", "vertex_ai", "vertex_ai-language-models":
		return PlatformGemini
	case "openai", "azure_openai":
		return PlatformOpenAI
	case "anthropic":
		return PlatformAnthropic
	case "newapi":
		return PlatformNewAPI
	case "antigravity":
		return PlatformAntigravity
	case "xai", "x-ai":
		// xAI / Grok (seventh platform). The overlay rows for the grok-imagine
		// media family + grok-code-fast-1 carry litellm_provider="xai"; mapping
		// it here lets the public-catalog servable gate and the availability
		// self-heal treat grok like the other native platforms.
		return PlatformGrok
	}
	return ""
}

// stateToAvailability converts an AvailabilityState to the JSON sub-object.
// Status="" (never-written state) maps to "untested" in the response.
func stateToAvailability(s AvailabilityState) *PublicCatalogAvailability {
	status := s.Status
	if status == "" {
		status = AvailabilityStatusUntested
	}
	a := &PublicCatalogAvailability{
		Status:          status,
		LastVerifiedAt:  s.LastSeenOKAt,
		LastCheckedAt:   s.LastCheckedAt,
		SampleCount24h:  s.SampleTotal24h,
		SuccessRate24h:  roundRate(s.SuccessRate24h()),
		LastFailureKind: s.LastFailureKind,
	}
	return a
}

// roundRate rounds to 4 decimal places (e.g. 0.9991 rather than 0.9991234...).
func roundRate(r float64) float64 {
	const factor = 1e4
	return float64(int(r*factor+0.5)) / factor
}
