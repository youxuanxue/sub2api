//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func activeRegistryBillingService(t *testing.T) *BillingService {
	t.Helper()
	resetPricingRegistrySnapshot(t)
	return NewBillingService(&config.Config{}, NewPricingService(&config.Config{}, nil))
}

func TestUS043_ImageTokenOwnerSettlesPositiveCost(t *testing.T) {
	svc := activeRegistryBillingService(t)
	for _, model := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1"} {
		require.True(t, svc.TkImageModelBillsByImageTokens(model), model)
		cost, err := svc.TkCalculateImageTokenCost(model, UsageTokens{
			InputTokens: 1000, ImageInputTokens: 200, ImageOutputTokens: 1500,
		}, 1)
		require.NoError(t, err, model)
		require.NotNil(t, cost, model)
		require.Positive(t, cost.TotalCost, model)
		require.Positive(t, cost.ImageOutputCost, model)
		require.Equal(t, string(BillingModeImage), cost.BillingMode, model)
	}
}

func TestUS043_ImageTokenOwnerMissingUsageFailsClosed(t *testing.T) {
	svc := activeRegistryBillingService(t)
	cost, err := svc.TkCalculateImageTokenCost("gpt-image-2", UsageTokens{InputTokens: 100}, 1)
	require.Nil(t, cost)
	require.ErrorIs(t, err, ErrImageUsageTokensUnavailable)
}

func TestUS043_BothGatewayImageFunnelsUseTokenSettlement(t *testing.T) {
	billing := activeRegistryBillingService(t)
	apiKey := &APIKey{Group: &Group{}}

	gateway := &GatewayService{billingService: billing}
	gatewayCost, err := gateway.calculateImageCost(
		context.Background(),
		&ForwardResult{Model: "gpt-image-2", ImageCount: 1, Usage: ClaudeUsage{ImageOutputTokens: 1500}},
		apiKey, "gpt-image-2", 1,
	)
	require.NoError(t, err)
	require.Positive(t, gatewayCost.TotalCost)
	require.Equal(t, string(BillingModeImage), gatewayCost.BillingMode)

	openAI := &OpenAIGatewayService{billingService: billing}
	openAICost, err := openAI.calculateOpenAIImageCost(
		context.Background(),
		"gpt-image-2",
		apiKey,
		&OpenAIForwardResult{ImageCount: 1, Usage: OpenAIUsage{ImageOutputTokens: 1500}},
		1,
	)
	require.NoError(t, err)
	require.Positive(t, openAICost.TotalCost)
	require.Equal(t, string(BillingModeImage), openAICost.BillingMode)
}

func TestUS043_GatewayMissingImageTokensReturnsBillingError(t *testing.T) {
	billing := activeRegistryBillingService(t)
	gateway := &GatewayService{billingService: billing}
	_, err := gateway.calculateImageCost(
		context.Background(),
		&ForwardResult{Model: "gpt-image-2", ImageCount: 1},
		&APIKey{Group: &Group{}}, "gpt-image-2", 1,
	)
	require.True(t, errors.Is(err, ErrImageUsageTokensUnavailable))
}
