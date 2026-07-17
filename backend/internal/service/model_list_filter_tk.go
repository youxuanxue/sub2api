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
// pricing is nil so that cold-start or degraded wiring never produces an
// empty model-list that would break an SDK.

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

// ServableClientFacingIDs returns the unified servable candidate IDs for a
// platform — the single source the gateway /v1/models family FALLBACK shares with
// the public /pricing catalog and the Your-Menu fallback. It is the empirical
// allowlist (or canonical when unprobed), pruned of structurally-gone ids and
// filtered to priced (billable), enforcing visible ⟹ reachable ∧ priced.
//
// This differs from FilterClientFacing in two intentional ways: (1) the candidate
// set is the curated servable allowlist (not an arbitrary caller-supplied list —
// used where there is no per-account model_mapping to honor), and (2) the
// availability prune is structurally-gone-only (matching /pricing), so a transient
// 5xx blip does not flap a model out of /v1/models while it stays in /pricing.
//
// Nil-safe: nil filter / nil pricing → no priced filter (fail-open); nil
// availability → no gone-prune. A nil *PricingAvailabilityService field is NOT
// promoted to a non-nil interface (that would panic the gone-prune on lookup).
func (f *ModelListFilter) ServableClientFacingIDs(ctx context.Context, platform string) []string {
	var avail MePricingAvailability
	var pricing *PricingCatalogService
	if f != nil {
		if f.availability != nil {
			avail = f.availability
		}
		pricing = f.pricing
	}
	return ServableClientFacingIDs(ctx, platform, avail, pricing)
}
