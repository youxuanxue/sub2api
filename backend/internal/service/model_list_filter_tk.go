package service

// TokenKey: client-facing model-list filter for Goal 2 of
// docs/approved/pricing-availability-source-of-truth.md §2.5.
//
// Used by three gateway handler endpoints:
//   GET /v1/models          (GatewayHandler.Models)
//   GET /v1beta/models      (GatewayHandler.GeminiV1BetaListModels)
//   GET /antigravity/models (GatewayHandler.AntigravityModels)
//
// Client-facing contract: only emit models that are (a) priced AND
// (b) not currently unreachable per the availability table. Fail-open when
// either service is nil so that cold-start or degraded wiring never produces
// an empty model-list that would break an SDK.

import "context"

// ModelListFilter gates client-visible model candidates against availability
// and pricing data. Both fields are nil-safe; see FilterClientFacing.
type ModelListFilter struct {
	pricing      *PricingCatalogService
	availability *PricingAvailabilityService
}

// NewModelListFilter constructs the filter. Both args may be nil (degraded /
// feature-flag-off mode — FilterClientFacing then fail-opens).
func NewModelListFilter(pricing *PricingCatalogService, availability *PricingAvailabilityService) *ModelListFilter {
	return &ModelListFilter{pricing: pricing, availability: availability}
}

// FilterClientFacing returns the subset of candidates that are priced AND
// not unreachable.
//
// Fail-open conditions (returns candidates unchanged):
//   - f == nil
//   - f.pricing == nil  (cold-start / degraded)
//   - len(candidates) == 0
//
// Availability filter is skipped (not dropped) when f.availability == nil,
// so that a partial wiring still produces a priced-only list rather than
// the full unfiltered one.
func (f *ModelListFilter) FilterClientFacing(ctx context.Context, platform string, candidates []string) []string {
	if f == nil || f.pricing == nil || len(candidates) == 0 {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if !f.pricing.IsModelPriced(id, platform) {
			continue
		}
		if f.availability != nil && f.availability.IsUnreachable(ctx, platform, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// PricedCandidates returns all priced model IDs from the catalog, suitable as
// the initial candidate set for platforms that don't derive candidates from
// account model_mappings (e.g. AntigravityModels).
//
// Returns nil when pricing is unavailable, allowing callers to fall back to
// static defaults (§5.x override-default policy).
func (f *ModelListFilter) PricedCandidates() []string {
	if f == nil || f.pricing == nil {
		return nil
	}
	return f.pricing.PricedModels()
}
