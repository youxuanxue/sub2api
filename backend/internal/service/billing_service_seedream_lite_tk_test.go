package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeedreamLiteFlatSettlementAndHold(t *testing.T) {
	svc := NewBillingService(nil, nil)
	const model = "doubao-seedream-5.0-lite"
	// Agent Plan list: CNY 0.22 per successful image, including 2K/4K.
	price := svc.tkRegistryMediaPricing(model)
	require.NotNil(t, price)
	require.InDelta(t, 0.22/6.7*1.06, price.OutputCostPerImage, 1e-12)
	for _, size := range []string{"1K", "2K", "4K", "", "auto"} {
		t.Run(size, func(t *testing.T) {
			cost := svc.CalculateImageCost(model, size, 3, nil, 1.7)
			require.InDelta(t, price.OutputCostPerImage*3, cost.TotalCost, 1e-12)
			require.InDelta(t, price.OutputCostPerImage*3*1.7, cost.ActualCost, 1e-12)
			require.InDelta(t, cost.ActualCost, svc.EstimateImageHold(model, size, 3, nil, 1.7), 1e-12)
		})
	}
	require.Zero(t, svc.CalculateImageCost(model, "2K", 0, nil, 1).ActualCost)
}

func TestSeedreamLitePreservesExplicitGroupPrice(t *testing.T) {
	svc := NewBillingService(nil, nil)
	price := 0.08
	group := &ImagePriceConfig{Price2K: &price}
	cost := svc.CalculateImageCost("doubao-seedream-5.0-lite", "2K", 2, group, 1.5)
	require.InDelta(t, price*2*1.5, cost.ActualCost, 1e-12)
	require.InDelta(t, cost.ActualCost, svc.EstimateImageHold("doubao-seedream-5.0-lite", "2K", 2, group, 1.5), 1e-12)
}
