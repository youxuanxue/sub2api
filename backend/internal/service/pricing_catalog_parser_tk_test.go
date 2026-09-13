package service

import (
	"encoding/json"
	"sort"
	"time"
)

// buildCatalogFromBytes is the pure parsing function — exported via package
// boundaries only for testing in pricing_catalog_tk_test.go. Robust to JSON
// malformations: an unparseable top-level returns empty; per-entry parse
// failures are skipped silently.
func buildCatalogFromBytes(data []byte, modTime time.Time) *PublicCatalogResponse {
	updatedAt := modTime
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	} else {
		updatedAt = updatedAt.UTC()
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return emptyPublicCatalog(updatedAt)
	}

	models := make([]PublicCatalogModel, 0, len(raw))
	for name, rawEntry := range raw {
		if name == "" || name == "sample_spec" {
			continue
		}
		var e catalogRichEntry
		if err := json.Unmarshal(rawEntry, &e); err != nil {
			continue
		}
		// Keep token-priced entries AND true media entries (per-image / per-second).
		// Media has no token price, so the original token-only guard dropped the
		// entire imagen-*/veo-*/seedream/seedance family. Chat rows may also
		// carry image-related price fields; those must not surface as empty
		// catalog rows unless they have token prices.
		if e.InputCostPerToken == nil && e.OutputCostPerToken == nil && catalogMediaBillingMode(&e) == "" {
			continue
		}
		models = append(models, catalogModelFromEntry(name, &e))
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ModelID < models[j].ModelID
	})

	return &PublicCatalogResponse{
		Object:    "list",
		Data:      models,
		UpdatedAt: updatedAt,
	}
}

// catalogFixtureForTest isolates projection consumers from registry contents.
// Cache seeding exercises the same read-side DTO without a production file seam.
func catalogFixtureForTest(data []byte) *PricingCatalogService {
	resp := buildCatalogFromBytes(data, time.Now())
	resp.membership = buildCatalogMembershipIndex(resp.Data)
	return &PricingCatalogService{cached: resp, cachedTk: loadTKPricingOverlaySnapshot()}
}
