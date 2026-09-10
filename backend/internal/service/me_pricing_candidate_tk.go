package service

import (
	"context"
	"sort"
)

// The menu enriches verified candidate support with official prices. Price
// rows and schedulable-account snapshots cannot independently grant support.
func (s *MePricingCatalogService) projectCandidateCatalog(ctx context.Context, key *APIKey, groups []Group, prices map[int64][]MePricingModel, metadata map[string]PublicCatalogModel) (map[int64][]MePricingModel, error) {
	origins := make(map[string]map[int64]Group)
	if key.IsUniversal() {
		if _, _, err := s.capabilities.discoverCandidates(ctx, key, UniversalProtocolAll, origins); err != nil {
			return nil, err
		}
	} else {
		for i := range groups {
			groupKey := *key
			groupKey.Group, groupKey.GroupID = &groups[i], &groups[i].ID
			if _, _, err := s.capabilities.discoverCandidates(ctx, &groupKey, UniversalProtocolAll, origins); err != nil {
				return nil, err
			}
		}
	}
	out := make(map[int64][]MePricingModel, len(groups))
	for _, group := range groups {
		priced := make(map[string]MePricingModel)
		for _, row := range prices[group.ID] {
			priced[row.ModelID] = row
		}
		out[group.ID] = []MePricingModel{}
		for model, authorized := range origins {
			if _, ok := authorized[group.ID]; !ok || !isMePricingModelDisplayed(model) {
				continue
			}
			row, exists := priced[model]
			if !exists {
				_, ok := lookupMePricingCatalogModel(model, metadata)
				if !ok {
					resolved := s.capabilities.candidateScopedPricing(ctx, []Group{group}, model)
					if resolved == nil || resolved.channelPricing == nil {
						continue
					}
					row = buildModelEntry(SupportedModel{Name: model, Pricing: resolved.channelPricing}, 1)
				} else {
					row = enrichMePricingCatalogModel(buildAccountFallbackEntry(model, 1, metadata), metadata)
				}
			}
			out[group.ID] = append(out[group.ID], row)
		}
		sort.Slice(out[group.ID], func(i, j int) bool { return out[group.ID][i].ModelID < out[group.ID][j].ModelID })
	}
	return out, nil
}
