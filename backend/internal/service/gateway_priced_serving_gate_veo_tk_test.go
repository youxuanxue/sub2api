//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// TestPricedServingGate_VeoGenerate001_DualOwnerContract pins the Veo incident
// ownership split (Feishu gate_rejected_unpriced on a token route is NOT a
// missing-overlay bug when the video surface already prices the model):
//   - overlay prices veo-3.1-generate-001 (TokenPricingAbsent + video tiers);
//   - TkVideoModelUnpriced is the /v1/video/generations admission owner and
//     must see that price;
//   - the token priced-serving gate (GetModelPricing) must reject video-only
//     TokenPricingAbsent rows so chat×veo cannot pass into
//     pricing_missing_record_zero_cost.
func TestPricedServingGate_VeoGenerate001_DualOwnerContract(t *testing.T) {
	const model = "veo-3.1-generate-001"

	pricingSvc := &PricingService{useActiveRegistry: true}
	lp := pricingSvc.GetModelPricing(model)
	require.NotNil(t, lp, "embedded overlay must contain %s", model)
	require.True(t, lp.TokenPricingAbsent, "production Veo rows omit token fields")
	require.False(t, tkIsEffectivelyUnpriced(lp))
	require.Greater(t, lp.OutputCostPerSecond, 0.0)

	billing := NewBillingService(&config.Config{}, pricingSvc)
	_, tokenErr := billing.GetModelPricing(model)
	require.ErrorIs(t, tokenErr, ErrModelPricingUnavailable,
		"token oracle must stay fail-closed for TokenPricingAbsent media")

	require.False(t, billing.TkVideoModelUnpriced(model),
		"video settlement / VideoSubmit guard must see the per-second / tier price")

	setting := newGateSettingService(PlatformNewAPI)
	require.True(t,
		tkPricedServingGateRejected(
			context.Background(),
			billing.GetModelPricing,
			nil,
			setting,
			model,
			PlatformNewAPI,
			0,
		),
		"chat/responses priced-serving gate must keep rejecting video-only Veo",
	)
	require.True(t, errors.Is(tokenErr, ErrModelPricingUnavailable))
}

// TestPricedServingGate_ImageOnlyTokenPricingAbsent_DualOwnerContract pins the
// same split for imagen-style rows: /v1/images/generations uses
// TkImageModelUnpriced (and forwardOpenAIV1JSON does not gate images/generations);
// the token priced-serving gate must still reject TokenPricingAbsent image-only
// rows so chat×imagen cannot settle at $0.
func TestPricedServingGate_ImageOnlyTokenPricingAbsent_DualOwnerContract(t *testing.T) {
	blob := []byte(`{
		"r3-image-priced": {"output_cost_per_image": 0.04, "mode": "image_generation", "litellm_provider": "test"}
	}`)
	billing := newConsistencyBilling(t, blob)
	const model = "r3-image-priced"

	lp := billing.pricingService.GetModelPricing(model)
	require.NotNil(t, lp)
	require.True(t, lp.TokenPricingAbsent)

	_, tokenErr := billing.GetModelPricing(model)
	require.ErrorIs(t, tokenErr, ErrModelPricingUnavailable)

	require.False(t, billing.TkImageModelUnpriced(model, nil, ""),
		"image settlement / image-generation guard must see the per-image price")
	setting := newGateSettingService(PlatformNewAPI)
	require.True(t,
		tkPricedServingGateRejected(
			context.Background(),
			billing.GetModelPricing,
			nil,
			setting,
			model,
			PlatformNewAPI,
			0,
		),
		"token priced-serving gate must reject image-only TokenPricingAbsent rows",
	)
}
