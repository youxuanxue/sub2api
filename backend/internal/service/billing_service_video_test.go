//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateVideoCost_PerSecondFromLiteLLM(t *testing.T) {
	svc := &BillingService{
		pricingService: &PricingService{
			pricingData: map[string]*LiteLLMModelPricing{
				// provider-prefixed key; bare name resolves via GetModelPricing fallback
				"gemini/veo-3.1-generate-preview": {OutputCostPerSecond: 0.40, Mode: "video_generation"},
			},
		},
	}

	// 8s × $0.40 = $3.20
	cost := svc.CalculateVideoCost("veo-3.1-generate-preview", VideoBillingResolution720P, 1, 8, nil, 1.0)
	require.InDelta(t, 3.20, cost.TotalCost, 1e-9)
	require.InDelta(t, 3.20, cost.ActualCost, 1e-9)
	require.Equal(t, "video", cost.BillingMode)

	// rate multiplier applies to ActualCost only
	cost = svc.CalculateVideoCost("veo-3.1-generate-preview", VideoBillingResolution720P, 1, 4, nil, 2.0)
	require.InDelta(t, 1.60, cost.TotalCost, 1e-9)
	require.InDelta(t, 3.20, cost.ActualCost, 1e-9)
}

func TestCalculateVideoCost_UnpricedModelIsZeroNotBlocking(t *testing.T) {
	svc := &BillingService{pricingService: &PricingService{pricingData: map[string]*LiteLLMModelPricing{}}}
	cost := svc.CalculateVideoCost("veo-unknown-model", VideoBillingResolution720P, 1, 8, nil, 1.0)
	require.Zero(t, cost.TotalCost)
	require.Zero(t, cost.ActualCost)
}

func TestCalculateVideoCost_NonPositiveSecondsClampedToOne(t *testing.T) {
	svc := &BillingService{
		pricingService: &PricingService{
			pricingData: map[string]*LiteLLMModelPricing{
				"gemini/veo-3.1-generate-preview": {OutputCostPerSecond: 0.40},
			},
		},
	}
	cost := svc.CalculateVideoCost("veo-3.1-generate-preview", VideoBillingResolution720P, 1, 0, nil, 1.0)
	require.InDelta(t, 0.40, cost.TotalCost, 1e-9) // clamped to 1s
}
