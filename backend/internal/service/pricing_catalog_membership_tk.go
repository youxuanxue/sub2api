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
	if s == nil {
		return false
	}
	id := strings.TrimSpace(modelID)
	if id == "" {
		return false
	}
	resp := s.BuildPublicCatalog(context.Background())
	if resp == nil {
		return false
	}
	for i := range resp.Data {
		if resp.Data[i].ModelID == id {
			return true
		}
	}
	if tail, ok := stripVendorPrefixForCatalogLookup(id); ok {
		allowPrefix := strings.Contains(tail, "-")
		prefix := tail + "-"
		for i := range resp.Data {
			mid := resp.Data[i].ModelID
			if mid == tail {
				return true
			}
			if allowPrefix && strings.HasPrefix(mid, prefix) {
				return true
			}
		}
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
