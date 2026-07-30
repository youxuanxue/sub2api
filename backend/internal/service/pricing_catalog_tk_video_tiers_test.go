//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachCatalogVideoPriceTiers_SeedanceMinTier(t *testing.T) {
	const modelID = "doubao-seedance-2-0-260128"
	hold := tkOverlayVideoHoldUnitPriceUSD(modelID)
	require.Greater(t, hold, 0.0)
	resp := &PublicCatalogResponse{
		Data: []PublicCatalogModel{{
			ModelID: modelID,
			Vendor:  "volcengine",
			Pricing: PublicCatalogPricing{BillingMode: "video", OutputCostPerSecond: hold + 0.01},
		}},
	}
	attachCatalogVideoPriceTiers(resp)
	require.NotEmpty(t, resp.Data[0].Pricing.VideoPriceTiers)
	min, ok := tkVideoMinUnitPriceUSD(modelID)
	require.True(t, ok)
	require.InDelta(t, min, resp.Data[0].Pricing.OutputCostPerSecond, 1e-9)
	require.Less(t, resp.Data[0].Pricing.OutputCostPerSecond, hold+0.01)
}
