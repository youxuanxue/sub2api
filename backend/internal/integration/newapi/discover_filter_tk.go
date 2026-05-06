package newapi

// TokenKey: upstream model-discovery filter pipeline.
// Spec: docs/approved/pricing-availability-source-of-truth.md §2.4 (R-002, Goal 1).
//
// Pipeline:
//   raw upstream /v1/models response
//     → DiscoveryFilter.Apply(...)
//        [1] drop ids whose provider metadata is explicitly unavailable
//        [2] drop ids whose model_availability cell = 'unreachable'
//        [3] tag pricing_status (priced | missing) — weak filter (admin sees missing)
//     → []DiscoveredModel
//
// Why this lives here (not in service): the integration/newapi package is
// TokenKey-owned (CLAUDE.md §4 — "New API integration logic lives in
// internal/integration/newapi/"). We use two narrow interfaces for the
// service-side dependencies because service already imports newapi (via
// admin_service_tk_newapi_save.go) — taking the concrete *service.PricingCatalogService
// type here would create an import cycle. The interfaces are an honest
// dependency-direction tool, not over-abstraction.

import (
	"context"
	"strings"
)

// PricingCatalogLookup is the read-side seam DiscoveryFilter needs from the
// pricing catalog service. The single production implementation is
// *service.PricingCatalogService (which exports IsModelPriced in
// pricing_catalog_membership_tk.go).
type PricingCatalogLookup interface {
	IsModelPriced(modelID, platform string) bool
}

// AvailabilityLookup is the read-side seam DiscoveryFilter needs from the
// pricing availability service. The single production implementation is
// *service.PricingAvailabilityService (which exports IsUnreachable in
// pricing_availability_predicate_tk.go).
type AvailabilityLookup interface {
	IsUnreachable(ctx context.Context, platform, modelID string) bool
}

// PricingStatus values. The two are mutually exclusive; "missing" is the
// intentional weak-filter signal that lets admin surface catalog gaps.
const (
	PricingStatusPriced  = "priced"
	PricingStatusMissing = "missing"
)

// DiscoveredModel is the post-filter shape returned to the admin "fetch
// upstream models" handler and to any future consumer (e.g. group dispatch
// dropdown). Keep the JSON tags stable — the admin UI binds to them.
type DiscoveredModel struct {
	ID            string `json:"id"`
	PricingStatus string `json:"pricing_status"`
}

// rawDiscoveredModel is the intermediate shape produced by per-provider
// fetchers. ProviderUnavailable lets a provider-specific fetcher signal
// "this id was returned but explicitly marked unavailable by the upstream
// metadata" (Gemini supportedGenerationMethods missing generateContent,
// OpenAI permission status=deprecated, etc.). The DiscoveryFilter drops
// entries with ProviderUnavailable=true at step [1].
type rawDiscoveredModel struct {
	ID                  string
	ProviderUnavailable bool
}

// DiscoveryFilter applies the three-step pipeline to a list of raw models
// fetched from an upstream provider. Both dependencies are nil-safe:
//   - pricing nil → step [3] tags everything as missing (defensive; in
//     production wire pricing is always non-nil — this just avoids panics
//     during cold-start / degraded test wiring).
//   - availability nil → step [2] is a no-op (feature-flag-off).
type DiscoveryFilter struct {
	pricing      PricingCatalogLookup
	availability AvailabilityLookup
}

// NewDiscoveryFilter wires the filter. Both args may be nil; see type doc.
func NewDiscoveryFilter(pricing PricingCatalogLookup, availability AvailabilityLookup) *DiscoveryFilter {
	return &DiscoveryFilter{pricing: pricing, availability: availability}
}

// Apply runs the three-step pipeline. platform is the TokenKey platform string
// (e.g. "newapi", "openai", "gemini") — used to scope the model_availability
// lookup. raw is the per-provider fetcher output.
//
// Output ordering preserves input ordering (deterministic admin UI).
//
// Behavior:
//   - nil receiver → returns input ids tagged as priced (degraded fail-open).
//   - empty raw → empty slice (never nil; preserves JSON array shape).
func (f *DiscoveryFilter) Apply(ctx context.Context, platform string, raw []rawDiscoveredModel) []DiscoveredModel {
	if len(raw) == 0 {
		return []DiscoveredModel{}
	}
	if f == nil {
		out := make([]DiscoveredModel, 0, len(raw))
		for _, r := range raw {
			out = append(out, DiscoveredModel{ID: r.ID, PricingStatus: PricingStatusPriced})
		}
		return out
	}

	platform = strings.TrimSpace(platform)
	out := make([]DiscoveredModel, 0, len(raw))
	for _, r := range raw {
		id := strings.TrimSpace(r.ID)
		if id == "" {
			continue
		}
		// [1] provider metadata says explicitly unavailable → drop
		if r.ProviderUnavailable {
			continue
		}
		// [2] model_availability table says unreachable → drop
		if f.availability != nil && f.availability.IsUnreachable(ctx, platform, id) {
			continue
		}
		// [3] tag pricing_status (weak filter)
		ps := PricingStatusMissing
		if f.pricing != nil && f.pricing.IsModelPriced(id, platform) {
			ps = PricingStatusPriced
		}
		out = append(out, DiscoveredModel{ID: id, PricingStatus: ps})
	}
	return out
}
