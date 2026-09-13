package service

// TokenKey: catalog-membership predicates shared by the upstream-discovery
// filter and client model-list filter.
//
// Why this lives in a TK companion file rather than pricing_catalog_tk.go:
// the catalog parsing/build code in the primary file is mostly upstream-shaped
// (LiteLLM JSON shape, US-028 contract). These predicates are TK-only consumers;
// keeping them isolated keeps merge surface minimal and makes it obvious where
// the membership semantics live.
//
// Membership indexes belong to the immutable catalog response, so file or
// registry rotation replaces prices and lookup decisions together.

import (
	"context"
	"strings"
)

// IsModelPriced reports whether modelID has a pricing entry in the catalog.
// The platform parameter is currently ignored because catalog membership is
// platform-agnostic. Note: <vendor>/<model>-style ids already carry their own
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
	index := resp.membership
	if index == nil {
		return nil, false
	}
	if i, ok := index.literal[id]; ok {
		if !isTkCuratedNewAPICatalogRowListed(resp.Data[i].Vendor, resp.Data[i].ModelID) {
			return nil, false
		}
		return &resp.Data[i], true
	}
	if tail, ok := stripVendorPrefixForCatalogLookup(id); ok {
		if i, found := index.fallback[tail]; found {
			return &resp.Data[i], true
		}
	}
	return nil, false
}

type catalogMembershipIndex struct {
	literal  map[string]int
	fallback map[string]int
}

func buildCatalogMembershipIndex(models []PublicCatalogModel) *catalogMembershipIndex {
	index := &catalogMembershipIndex{
		literal:  make(map[string]int, len(models)),
		fallback: make(map[string]int, len(models)),
	}
	for i := range models {
		id := models[i].ModelID
		if _, exists := index.literal[id]; !exists {
			index.literal[id] = i
		}
		if !isTkCuratedNewAPICatalogRowListed(models[i].Vendor, id) {
			continue
		}
		if _, exists := index.fallback[id]; !exists {
			index.fallback[id] = i
		}
		// The old fallback scan chose the first exact OR version-prefix match
		// in catalog order. Keep that order, including duplicate and hidden rows.
		firstDash := strings.IndexByte(id, '-')
		if firstDash < 0 {
			continue
		}
		for j := firstDash + 1; j < len(id); j++ {
			if id[j] != '-' {
				continue
			}
			prefix := id[:j]
			if _, exists := index.fallback[prefix]; !exists {
				index.fallback[prefix] = i
			}
		}
	}
	return index
}

// NOTE: the runtime priced-serving gate (docs/approved/priced-or-it-doesnt-ship.md)
// does NOT use a catalog predicate. It asks billing's own oracle
// (BillingService.GetModelPricing → ErrModelPricingUnavailable) on the exact key
// billing will charge, so "gate ⟺ billing" holds by construction (no shadow
// predicate to drift). An earlier draft had a stricter catalog predicate here; it
// was removed once the gate moved to the billing oracle (R3 dissolved). findCatalogModel
// above stays — it backs IsModelPriced (model-list / discovery membership).

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
