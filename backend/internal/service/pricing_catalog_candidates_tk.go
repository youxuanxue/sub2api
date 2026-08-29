package service

import (
	"context"
	"fmt"
)

// Shared per-platform catalog candidates for the admin model-whitelist selector
// (admin_service GetGroupModelsListCandidates) and the per-user menu fallback
// (me_pricing platformDefaultModelIDs). Both read the curated catalog set and
// prune structurally-gone evidence. Canonical advertised lists are not a catalog owner.

// tkServableCandidateIDs returns the self-healing candidate list for one platform
// (used by the admin selector). Empirical native platforms draw from
// supportedCatalogModelIDsForPlatform; newapi keeps its canonical/channel-shaped
// defaults. Every platform is then pruned of structurally-gone models
// (tkPruneStructurallyGoneIDs), so the result stays platform-scoped:
// a model gone on anthropic stays on antigravity if it is still servable there.
// availability == nil → no prune.
func tkServableCandidateIDs(ctx context.Context, platform string, availability MePricingAvailability) []string {
	var ids []string
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformGrok:
		// Grok has no canonical DefaultModels list — without this case it fell
		// to the default arm below and leaked claude.DefaultModels into the grok
		// group's admin model-whitelist selector. Its empirical allowlist (the
		// priced overlay set) is the only correct source.
		ids = supportedCatalogModelIDsForPlatform(platform)
	case PlatformAntigravity:
		// Probed Antigravity set when populated; canonical fallback when unprobed
		// (supportedCatalogModelIDsForPlatform returns nil for an empty set).
		if ids = supportedCatalogModelIDsForPlatform(platform); len(ids) == 0 {
			ids = defaultModelsListCandidateIDs(platform)
		}
	case PlatformGemini:
		// Probed gemini set when populated; canonical fallback when unprobed
		// (supportedCatalogModelIDsForPlatform returns nil for an empty set).
		if ids = supportedCatalogModelIDsForPlatform(platform); len(ids) == 0 {
			ids = defaultModelsListCandidateIDs(platform)
		}
	default:
		// newapi / unknown — no empirical allowlist; canonical.
		ids = defaultModelsListCandidateIDs(platform)
	}
	return tkPruneStructurallyGoneIDs(ctx, platform, ids, availability)
}

// tkPruneStructurallyGoneIDs drops model IDs that live model_availability reports
// as structurally gone (model_not_found → unreachable; see
// tkAvailabilityStructurallyGone). Shared by the admin selector and the per-user
// menu fallback. Nil-safe: availability == nil (tests / Phase-1) → passthrough.
func tkPruneStructurallyGoneIDs(ctx context.Context, platform string, ids []string, availability MePricingAvailability) []string {
	if availability == nil || len(ids) == 0 {
		return ids
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		st, err := availability.GetAvailability(ctx, platform, id)
		if err == nil && tkAvailabilityStructurallyGone(st) {
			continue
		}
		kept = append(kept, id)
	}
	return kept
}

// ServableClientFacingIDs is the shared client-facing catalog projection used by
// the public /pricing filter, Your-Menu fallback, and gateway model-list fallback.
// It enforces the catalog invariant
//
//	displayed ⟹ catalog-approved ∧ priced ∧ not structurally gone
//
// by taking the per-platform catalog candidate set (tkServableCandidateIDs:
// empirical allowlist or canonical, with structurally-gone ids pruned) and keeping
// only ids that resolve to a usable price (IsModelPriced — the billing-capability
// condition). This closes the "in the allowlist but unpriced → advertised at $0" hole
// structurally rather than ASSUMING allowlist ⊆ priced (e.g. an allowlisted but
// price-less id like tab_flash_lite_preview is dropped here). This projection does
// not claim that a concrete request has a legal RequestPlan or current capacity.
//
// Nil-safe, matching the surrounding fail-open posture:
//   - availability == nil → no structurally-gone prune (cold-start / tests)
//   - pricing == nil      → no priced filter (cold-start / degraded wiring), so a
//     broken pricing source never collapses a model-list to empty and breaks SDKs.
func ServableClientFacingIDs(ctx context.Context, platform string, availability MePricingAvailability, pricing *PricingCatalogService) []string {
	ids := tkServableCandidateIDs(ctx, platform, availability)
	if pricing == nil || len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if pricing.IsModelPriced(id, platform) {
			out = append(out, id)
		}
	}
	return out
}

func servableClientFacingIDsStrict(ctx context.Context, platform string, availability MePricingAvailability, pricing *PricingCatalogService) ([]string, error) {
	ids := tkServableCandidateIDs(ctx, platform, nil)
	if pricing != nil {
		priced := make([]string, 0, len(ids))
		for _, id := range ids {
			if pricing.IsModelPriced(id, platform) {
				priced = append(priced, id)
			}
		}
		ids = priced
	}
	if availability == nil || len(ids) == 0 {
		return ids, nil
	}

	if batchAvailability, ok := availability.(interface {
		GetAvailabilityBatch(context.Context, string, []string) (map[string]AvailabilityState, error)
	}); ok {
		states, err := batchAvailability.GetAvailabilityBatch(ctx, platform, ids)
		if err != nil {
			return nil, fmt.Errorf("read model availability for %s: %w", platform, err)
		}
		kept := make([]string, 0, len(ids))
		for _, id := range ids {
			if !tkAvailabilityStructurallyGone(states[id]) {
				kept = append(kept, id)
			}
		}
		return kept, nil
	}

	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		state, err := availability.GetAvailability(ctx, platform, id)
		if err != nil {
			return nil, fmt.Errorf("read model availability for %s/%s: %w", platform, id, err)
		}
		if !tkAvailabilityStructurallyGone(state) {
			kept = append(kept, id)
		}
	}
	return kept, nil
}
