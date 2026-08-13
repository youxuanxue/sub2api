//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelPricingResolver_TkResolveOverlayMediaPerRequest(t *testing.T) {
	pricingSvc := &PricingService{}
	data, err := pricingSvc.parsePricingData([]byte(`{
		"imagen-4.0-generate-001": {
			"output_cost_per_image": 0.04,
			"mode": "image_generation",
			"litellm_provider": "vertex_ai"
		},
		"gpt-4o-mini": {
			"input_cost_per_token": 0.00000015,
			"output_cost_per_token": 0.0000006
		}
	}`))
	require.NoError(t, err)
	pricingSvc.pricingData = data

	billing := NewBillingService(nil, pricingSvc)
	resolver := NewModelPricingResolver(nil, billing)

	media := resolver.tkResolveOverlayMediaPerRequest("imagen-4.0-generate-001")
	require.NotNil(t, media)
	require.Equal(t, BillingModeImage, media.Mode)
	require.InDelta(t, 0.04, media.DefaultPerRequestPrice, 1e-12)
	require.Equal(t, PricingSourceLiteLLM, media.Source)

	require.Nil(t, resolver.tkResolveOverlayMediaPerRequest("gpt-4o-mini"))
	require.Nil(t, resolver.tkResolveOverlayMediaPerRequest("unknown-model"))
}

func TestModelPricingResolver_Resolve_ImageOnlyOverlayOnTokenPath(t *testing.T) {
	pricingSvc := &PricingService{}
	data, err := pricingSvc.parsePricingData([]byte(`{
		"imagen-4.0-fast-generate-001": {
			"output_cost_per_image": 0.02,
			"mode": "image_generation",
			"litellm_provider": "vertex_ai"
		}
	}`))
	require.NoError(t, err)
	pricingSvc.pricingData = data

	billing := NewBillingService(nil, pricingSvc)
	resolver := NewModelPricingResolver(nil, billing)

	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "imagen-4.0-fast-generate-001"})
	require.NotNil(t, resolved)
	require.Equal(t, BillingModeImage, resolved.Mode)
	require.InDelta(t, 0.02, resolved.DefaultPerRequestPrice, 1e-12)
	require.Equal(t, PricingSourceLiteLLM, resolved.Source)

	cost, err := billing.CalculateCostUnified(CostInput{
		Model:          "imagen-4.0-fast-generate-001",
		RequestCount:   1,
		RateMultiplier: 1.0,
		Resolver:       resolver,
	})
	require.NoError(t, err)
	require.NotNil(t, cost)
	require.InDelta(t, 0.02, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.02, cost.ActualCost, 1e-12)
}

func TestOpenAIGatewayService_CalculateRecordUsageCost_ImagenTokenPathUsesOverlayPerImage(t *testing.T) {
	pricingSvc := &PricingService{}
	data, err := pricingSvc.parsePricingData([]byte(`{
		"imagen-4.0-generate-001": {
			"output_cost_per_image": 0.04,
			"mode": "image_generation",
			"litellm_provider": "vertex_ai"
		}
	}`))
	require.NoError(t, err)
	pricingSvc.pricingData = data

	billing := NewBillingService(nil, pricingSvc)
	resolver := NewModelPricingResolver(nil, billing)
	svc := &OpenAIGatewayService{billingService: billing, resolver: resolver}

	apiKey := &APIKey{Group: &Group{ID: 16, Platform: PlatformNewAPI}}
	result := &OpenAIForwardResult{
		Model:         "imagen-4.0-generate-001",
		UpstreamModel: "imagen-4.0-generate-001",
		Usage: OpenAIUsage{
			InputTokens:  200,
			OutputTokens: 58,
		},
		ImageCount: 0,
	}

	cost, err := svc.calculateOpenAIRecordUsageCost(
		context.Background(),
		result,
		apiKey,
		[]string{"imagen-4.0-generate-001"},
		1.0, 1.0, 1.0, 1.0,
		UsageTokens{InputTokens: 200, OutputTokens: 58},
		"", boolPtr(false),
	)
	require.NoError(t, err)
	require.NotNil(t, cost)
	require.InDelta(t, 0.04, cost.TotalCost, 1e-12)
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
}
