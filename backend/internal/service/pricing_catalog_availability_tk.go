package service

import "context"

// DecorateWithAvailability overlays per-model availability data from
// PricingAvailabilityService onto a catalog response copy. Called by the
// pricing catalog handler AFTER BuildPublicCatalog so that:
//   - The base catalog mtime-cache stays cheap and hot.
//   - Availability data is fetched fresh on every /pricing request
//     (the per-cell Redis cache at the availability service layer amortises PG
//     read cost; that cache is wired in PR-2/PR-3).
//
// Mapping: /pricing responds models without a platform dimension. We infer
// platform from the vendor/litellm_provider field. For now only "gemini"
// and other known platforms are mapped; unknown vendors produce no badge.
//
// Phase-1 behaviour: svc == nil → returns the base resp unchanged (feature
// flag effectively off). Clients see no `availability` field.
func DecorateWithAvailability(ctx context.Context, resp *PublicCatalogResponse, svc *PricingAvailabilityService) *PublicCatalogResponse {
	if svc == nil || resp == nil || len(resp.Data) == 0 {
		return resp
	}

	// Shallow-copy the slice so the cached response isn't mutated.
	decorated := &PublicCatalogResponse{
		Object:    resp.Object,
		UpdatedAt: resp.UpdatedAt,
		Data:      make([]PublicCatalogModel, len(resp.Data)),
	}
	copy(decorated.Data, resp.Data)

	for i, model := range decorated.Data {
		platform := inferPlatformFromVendor(model.Vendor)
		if platform == "" {
			continue
		}
		state, err := svc.GetAvailability(ctx, platform, model.ModelID)
		if err != nil {
			continue
		}
		decorated.Data[i].Availability = stateToAvailability(state)
	}

	return decorated
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
