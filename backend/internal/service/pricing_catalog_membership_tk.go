package service

// TokenKey: catalog-membership predicates for the upstream-discovery filter
// (R-002, Goal 1) and client model-list filter (R-003, Goal 2).
//
// Why this lives in a TK companion file rather than pricing_catalog_tk.go:
// the catalog parsing/build code in the primary file is mostly upstream-shaped
// (LiteLLM JSON shape, US-028 contract). These predicates are TK-only consumers;
// keeping them isolated keeps merge surface minimal and makes it obvious where
// the membership semantics live.
//
// Performance: we call BuildPublicCatalog which is mtime-cached, so repeated
// calls within the same source-mtime window do NOT re-parse the JSON. For the
// admin "fetch upstream models" path (one call per click) and the client
// /v1/models path (one call per model-list request) this is fine; if hot
// paths ever pull this in a tight loop, switch to a model_id → bool map
// computed lazily and invalidated on cache rotation.

import (
	"context"
	"strings"
)

// IsModelPriced reports whether modelID has a pricing entry in the catalog.
// The platform parameter is reserved for a future *cross-vendor* pricing
// split — e.g. claude-3-haiku-20240307 priced differently on Bedrock vs
// anthropic.com (see §8 of docs/approved/pricing-availability-source-of-truth.md
// "遗留事项") — and is currently ignored; the catalog is platform-agnostic
// in v1. Note: <vendor>/<model>-style ids already carry their own
// per-vendor signal via the "/" prefix, which the fallback below consumes
// without needing the platform argument.
//
// Behavior:
//   - nil receiver, empty modelID, or empty/cold catalog → false (callers
//     interpret false as "not priced", which the upstream-discovery filter
//     uses to tag pricing_status="missing" and the client model-list filter
//     uses for fail-open semantics — see ModelListFilter.FilterClientFacing).
//
// Vendor-namespaced fallback: OpenRouter (and Azure/Vertex/Bedrock-style
// proxies) report model ids as "<vendor>/<family-version>" — e.g.
// "anthropic/claude-3-haiku", "anthropic/claude-opus-4.5". The catalog
// (LiteLLM-shaped JSON) keys models on the bare name and uses "-" instead of
// "." in version segments — e.g. "claude-3-haiku-20240307",
// "claude-opus-4-5-20251001". When the literal lookup fails AND the id
// contains a single "/", we strip the vendor prefix, normalize "." → "-",
// and try again: first as an exact match against the catalog, then as a
// version-suffix prefix match ("<tail>-*"). The prefix match requires tail
// to contain at least one "-" to prevent a family-level id (e.g.
// "openai/gpt") from being treated as priced just because some specific
// variant exists.
func (s *PricingCatalogService) IsModelPriced(modelID, platform string) bool {
	_, ok := s.findCatalogModel(modelID)
	return ok
}

// findCatalogModel resolves modelID to its PublicCatalogModel using the literal
// + vendor-prefix-fallback lookup shared by IsModelPriced and the serving-gate
// effective-priced predicate. Returns (nil, false) for a nil receiver, empty
// id, cold catalog, or no match.
func (s *PricingCatalogService) findCatalogModel(modelID string) (*PublicCatalogModel, bool) {
	if s == nil {
		return nil, false
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		return nil, false
	}
	resp := s.BuildPublicCatalog(context.Background())
	if resp == nil {
		return nil, false
	}
	for i := range resp.Data {
		if resp.Data[i].ModelID == id {
			return &resp.Data[i], true
		}
	}
	if tail, ok := stripVendorPrefixForCatalogLookup(id); ok {
		allowPrefix := strings.Contains(tail, "-")
		prefix := tail + "-"
		for i := range resp.Data {
			mid := resp.Data[i].ModelID
			if mid == tail {
				return &resp.Data[i], true
			}
			if allowPrefix && strings.HasPrefix(mid, prefix) {
				return &resp.Data[i], true
			}
		}
	}
	return nil, false
}

// tkIsModelEffectivelyPriced is the runtime priced-serving gate's predicate
// (docs/approved/priced-or-it-doesnt-ship.md §7 R3). It is STRICTER than
// IsModelPriced: catalog *membership* is necessary but not sufficient — the
// matched entry must also carry a non-zero resolvable price.
//
// Why a separate, stricter predicate instead of reusing IsModelPriced: the
// billing resolver (BillingService.GetModelPricing) drops an all-zero token
// entry via tkIsEffectivelyUnpriced and returns ErrModelPricingUnavailable,
// then bills $0. But buildCatalogFromBytes KEEPS an entry whose token-cost
// pointers are present-but-zero, so IsModelPriced returns true for it. A gate
// built on bare membership would therefore PASS a model billing charges $0 for
// — "闸形同虚设" (R3 predicate drift). This predicate mirrors
// tkIsEffectivelyUnpriced: a model is effectively priced iff it has a non-zero
// token price OR a non-zero media (per-image / per-second) price. The R3 test
// pins `tkIsModelEffectivelyPriced(m) ⟺ GetModelPricing(m) != ErrModelPricingUnavailable`
// on the candidate set, including the present-but-zero boundary.
//
// IsModelPriced is intentionally left unchanged — it feeds the model-list /
// upstream-discovery filters whose membership semantics predate this gate;
// tightening it there is out of scope for v1 (separate blast radius).
func (s *PricingCatalogService) tkIsModelEffectivelyPriced(modelID, platform string) bool {
	m, ok := s.findCatalogModel(modelID)
	if !ok || m == nil {
		return false
	}
	p := m.Pricing
	// Token-priced: any non-zero per-token rate (input/output/thinking/cache).
	if p.InputPer1KTokens != 0 || p.OutputPer1KTokens != 0 ||
		p.ThinkingOutputPer1KTokens != 0 || p.CacheReadPer1K != 0 || p.CacheWritePer1K != 0 {
		return true
	}
	// Media-priced: per-image (image gen) or per-second (video gen). These models
	// legitimately carry zero token price; the media unit is the real price.
	if p.OutputCostPerImage != 0 || p.OutputCostPerSecond != 0 {
		return true
	}
	return false
}

// stripVendorPrefixForCatalogLookup converts an OpenRouter/Azure-style
// "<vendor>/<model>" id into the bare catalog form, normalizing "." → "-"
// in the model segment (LiteLLM catalog uses "-" everywhere). Returns
// (tail, true) only when exactly one "/" is present and both sides are
// non-empty — multi-segment ids ("a/b/c") are too ambiguous to map safely.
func stripVendorPrefixForCatalogLookup(id string) (string, bool) {
	slash := strings.IndexByte(id, '/')
	if slash <= 0 || slash >= len(id)-1 {
		return "", false
	}
	if strings.IndexByte(id[slash+1:], '/') >= 0 {
		return "", false
	}
	tail := strings.ReplaceAll(id[slash+1:], ".", "-")
	if tail == "" {
		return "", false
	}
	return tail, true
}
