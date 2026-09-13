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

// IsModelPriced reports catalog price membership, independent of platform.
// Qualified IDs reuse billing's exact key spellings and the catalog snapshot's
// aliases before legacy vendor/date-prefix compatibility. Missing membership
// filters client model lists and tags admin discovery as missing pricing.
func (s *PricingCatalogService) IsModelPriced(modelID, platform string) bool {
	_, ok := s.findCatalogModel(modelID)
	return ok
}

// findCatalogModel resolves a price row from one immutable catalog snapshot.
// Request admission continues to use billing's oracle, not catalog membership.
func (s *PricingCatalogService) findCatalogModel(modelID string) (*PublicCatalogModel, bool) {
	if s == nil {
		return nil, false
	}
	id := strings.ToLower(strings.TrimSpace(modelID))
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
	for _, candidate := range catalogModelLookupCandidates(id) {
		if i, ok := index.literal[candidate]; ok {
			if !isTkCuratedNewAPICatalogRowListed(resp.Data[i].Vendor, resp.Data[i].ModelID) {
				return nil, false
			}
			return &resp.Data[i], true
		}
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

// catalogModelLookupCandidates preserves the catalog's single-namespace boundary.
// Exact owner/alias spellings come from billing; dotted-to-dashed compatibility
// is attempted only after those exact candidates, never instead of them.
func catalogModelLookupCandidates(modelID string) []string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if _, ok := stripVendorPrefixForCatalogLookup(id); !ok {
		return []string{id}
	}
	return buildModelLookupCandidates(id)
}

// stripVendorPrefixForCatalogLookup is the legacy spelling fallback, after
// exact owners and aliases. Only a nonempty single namespace is accepted.
func stripVendorPrefixForCatalogLookup(id string) (string, bool) {
	slash := strings.IndexByte(id, '/')
	if slash <= 0 || slash >= len(id)-1 || strings.Contains(id[slash+1:], "/") {
		return "", false
	}
	return strings.ReplaceAll(id[slash+1:], ".", "-"), true
}
