//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeedream50ProImageTierPricing(t *testing.T) {
	t.Parallel()
	const model = "doubao-seedream-5-0-pro"
	svc := newTestBillingService()
	group := &ImagePriceConfig{}
	tax := tkOfficialListBaseTaxMultiplier()

	cost1K := svc.CalculateImageCost(model, "1K", 1, group, 1.0)
	require.InDelta(t, 0.30/6.7*tax, cost1K.ActualCost, 1e-12)

	cost2K := svc.CalculateImageCost(model, "2K", 1, group, 1.0)
	require.InDelta(t, 0.60/6.7*tax, cost2K.ActualCost, 1e-12)

	cost := svc.CalculateImageCost(model, "2K", 2, group, 1.5)
	require.InDelta(t, cost.ActualCost, svc.EstimateImageHold(model, "2K", 2, group, 1.5), 1e-12)
}
