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
	if seedance := tkSeedanceVideoCatalogTiers(modelID); len(seedance) > 0 {
		out := make([]PublicCatalogVideoTier, 0, len(seedance))
		for _, row := range seedance {
			tier := PublicCatalogVideoTier{
				Resolution:      row.Resolution,
				PerSecond:       row.PerSecond,
				DefaultForModel: row.DefaultForModel,
			}
			if row.PerSecondSilent > 0 {
				s := row.PerSecondSilent
				tier.PerSecondSilent = &s
			}
			out = append(out, tier)
		}
		return out
	}
	if veo := tkVeoVideoCatalogTiers(modelID); len(veo) > 0 {
		out := make([]PublicCatalogVideoTier, 0, len(veo))
		for _, row := range veo {
			tier := PublicCatalogVideoTier{
				Resolution:      row.Resolution,
				PerSecond:       row.PerSecond,
				DefaultForModel: row.DefaultForModel,
			}
			if row.PerSecondSilent > 0 {
				s := row.PerSecondSilent
				tier.PerSecondSilent = &s
			}
			out = append(out, tier)
		}
		return out
	}
	if grok := tkGrokImagineVideoCatalogTiers(modelID); len(grok) > 0 {
		out := make([]PublicCatalogVideoTier, 0, len(grok))
		for _, row := range grok {
			tier := PublicCatalogVideoTier{
				Resolution:      row.Resolution,
				PerSecond:       row.PerSecond,
				DefaultForModel: row.DefaultForModel,
			}
			if row.InputImageSurchargePerSecond > 0 {
				s := row.InputImageSurchargePerSecond
				tier.InputImageSurchargePerSecond = &s
			}
			out = append(out, tier)
		}
		return out
	}
	return nil
}
