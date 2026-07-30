package service

// PublicCatalogVideoTier is one resolution (and optional audio) bracket for video models.
type PublicCatalogVideoTier struct {
	Resolution                   string   `json:"resolution"`
	PerSecond                    float64  `json:"per_second"`
	PerSecondSilent              *float64 `json:"per_second_silent,omitempty"`
	InputImageSurchargePerSecond *float64 `json:"input_image_surcharge_per_second,omitempty"`
	DefaultForModel              bool     `json:"default_for_model,omitempty"`
}

func attachCatalogVideoPriceTiers(resp *PublicCatalogResponse) {
	if resp == nil || len(resp.Data) == 0 {
		return
	}
	for i := range resp.Data {
		modelID := resp.Data[i].ModelID
		if resp.Data[i].Pricing.BillingMode != "video" {
			continue
		}
		tiers := buildPublicCatalogVideoTiers(modelID)
		if len(tiers) == 0 {
			continue
		}
		resp.Data[i].Pricing.VideoPriceTiers = tiers
		if min, ok := tkVideoMinUnitPriceUSD(modelID); ok && min > 0 {
			resp.Data[i].Pricing.OutputCostPerSecond = min
		}
	}
}

func buildPublicCatalogVideoTiers(modelID string) []PublicCatalogVideoTier {
	return tkOverlayVideoCatalogTiers(modelID)
}
