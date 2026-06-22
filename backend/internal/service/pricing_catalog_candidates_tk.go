package service

import "context"

// Self-healing per-platform candidate model IDs — the single source of truth
// behind BOTH the admin model-whitelist selector (admin_service
// GetGroupModelsListCandidates) and the per-user menu fallback (me_pricing
// platformDefaultModelIDs). Before this, the admin selector drew from the
// CANONICAL defaults (claude.DefaultModels etc., which still list retired /
// access-gated models like claude-fable-5), so the frontend had to hand-maintain
// a hardcoded mirror. Now both draw from the empirically-servable allowlist with
// live model_availability pruning, so an upstream-retired model auto-drops from
// the selector without a manual edit. (R-003 follow-up to PR #752.)

// tkServableCandidateIDs returns the self-healing candidate list for one platform
// (used by the admin selector). Empirical platforms (anthropic/openai, and gemini
// once probed) draw from supportedCatalogModelIDsForPlatform; antigravity/newapi
// have no empirical allowlist and keep their canonical defaults. Every platform
// is then pruned of structurally-gone models (tkPruneStructurallyGoneIDs), so the
// result respects PER-PLATFORM truth: a model gone on anthropic stays on
// antigravity if it is still servable there. availability == nil → no prune.
func tkServableCandidateIDs(ctx context.Context, platform string, availability MePricingAvailability) []string {
	var ids []string
	switch platform {
	case PlatformAnthropic, PlatformOpenAI, PlatformGrok:
		// Grok has no canonical DefaultModels list — without this case it fell
		// to the default arm below and leaked claude.DefaultModels into the grok
		// group's admin model-whitelist selector. Its empirical allowlist (the
		// priced overlay set) is the only correct source.
		ids = supportedCatalogModelIDsForPlatform(platform)
	case PlatformGemini:
		// Probed gemini set when populated; canonical fallback when unprobed
		// (supportedCatalogModelIDsForPlatform returns nil for an empty set).
		if ids = supportedCatalogModelIDsForPlatform(platform); len(ids) == 0 {
			ids = defaultModelsListCandidateIDs(platform)
		}
	default:
		// antigravity / newapi / unknown — no empirical allowlist; canonical.
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

// ServableClientFacingIDs is the SINGLE client-facing servable truth shared by
// every model-list surface — the public /pricing catalog filter, the Your-Menu
// fallback, and the gateway /v1/models family fallback (/v1/models, /v1beta/models,
// /antigravity/models). It enforces the invariant
//
//	visible ⟹ reachable ∧ priced
//
// by taking the per-platform servable candidate set (tkServableCandidateIDs:
// empirical allowlist or canonical, with structurally-gone ids pruned) and keeping
// only ids that resolve to a usable price (IsModelPriced — the billing-capability
// gate). This closes the "in the allowlist but unpriced → advertised at $0" hole
// structurally rather than ASSUMING allowlist ⊆ priced (e.g. an allowlisted but
// price-less id like tab_flash_lite_preview is dropped here, not silently served
// for free). It is the same priced gate FilterClientFacing applies to the
// account-mapped path, so fallback and account-mapped surfaces agree.
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
