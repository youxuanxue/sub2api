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
// The platform parameter is reserved for a future per-platform pricing split
// (see §8 of docs/approved/pricing-availability-source-of-truth.md "遗留事项")
// and is currently ignored; the catalog is platform-agnostic in v1.
//
// Behavior:
//   - nil receiver, empty modelID, or empty/cold catalog → false (callers
//     interpret false as "not priced", which the upstream-discovery filter
//     uses to tag pricing_status="missing" and the client model-list filter
//     uses for fail-open semantics — see ModelListFilter.FilterClientFacing).
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
	return false
}

// PricedModels returns the catalog's full priced model_id set as a sorted slice.
// Used by AntigravityModels handler when the candidate pool is empty (catalog
// becomes the override-default source per §5.x).
//
// Behavior:
//   - nil receiver / empty catalog → empty slice (never nil; preserves
//     downstream JSON serialization stability).
func (s *PricingCatalogService) PricedModels() []string {
	if s == nil {
		return []string{}
	}
	resp := s.BuildPublicCatalog(context.Background())
	if resp == nil || len(resp.Data) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(resp.Data))
	for i := range resp.Data {
		out = append(out, resp.Data[i].ModelID)
	}
	return out
}
