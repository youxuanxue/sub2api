//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachCatalogVideoPriceTiers_SeedanceMinTier(t *testing.T) {
	const modelID = "doubao-seedance-2-0-260128"
	min, ok := tkVideoMinUnitPriceUSD(modelID)
	require.True(t, ok)
	staleFlatPrice := min + 0.01
	resp := &PublicCatalogResponse{
		Data: []PublicCatalogModel{{
			ModelID: modelID,
			Vendor:  "volcengine",
			Pricing: PublicCatalogPricing{BillingMode: "video", OutputCostPerSecond: staleFlatPrice},
		}},
	}
	attachCatalogVideoPriceTiersFromSnapshot(resp, loadTKPricingOverlaySnapshot())
	require.NotEmpty(t, resp.Data[0].Pricing.VideoPriceTiers)
	require.InDelta(t, min, resp.Data[0].Pricing.OutputCostPerSecond, 1e-9)
	require.Less(t, resp.Data[0].Pricing.OutputCostPerSecond, staleFlatPrice)
}
