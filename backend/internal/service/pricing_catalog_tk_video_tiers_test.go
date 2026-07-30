//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAttachCatalogVideoPriceTiers_SeedanceMinTier(t *testing.T) {
	resp := &PublicCatalogResponse{
		Data: []PublicCatalogModel{{
			ModelID: "doubao-seedance-2-0-260128",
			Vendor:  "volcengine",
			Pricing: PublicCatalogPricing{BillingMode: "video", OutputCostPerSecond: 0.6},
		}},
	}
	attachCatalogVideoPriceTiers(resp)
	require.NotEmpty(t, resp.Data[0].Pricing.VideoPriceTiers)
	require.Less(t, resp.Data[0].Pricing.OutputCostPerSecond, 0.6)
}
