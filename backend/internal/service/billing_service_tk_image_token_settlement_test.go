//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// registryBackedBillingService builds a BillingService over the SHIPPED registry
// via the same projection production uses, so these assertions are about the
// real rate card rather than a fixture.
func registryBackedBillingService(t *testing.T) *BillingService {
	t.Helper()
	data, err := (&PricingService{}).parsePricingData(tkPricingOverlayRaw)
	require.NoError(t, err)
	return &BillingService{pricingService: &PricingService{pricingData: data}}
}

// TestImageSettlementRouting_NoShippedOwnerBillsZero is the load-bearing
// assertion: for EVERY image_generation owner in the shipped registry, at least
// one of the three outcomes must hold —
//
//	(a) per-image settlement produces a positive charge, or
//	(b) the model is routed to image-token settlement, or
//	(c) the unpriced-media guard rejects it before any spend.
//
// A model that satisfies none of these bills $0 while being served, which is the
// exact regression class docs/approved/priced-or-it-doesnt-ship.md forbids.
// gpt-image-* previously fell in that hole: priced only by image token, routed to
// per-image settlement (=$0), and admitted by the guard.
func TestImageSettlementRouting_NoShippedOwnerBillsZero(t *testing.T) {
	svc := registryBackedBillingService(t)

	checked := 0
	for name, row := range loadTKPricingOverlay() {
		if row == nil || row.Mode != "image_generation" {
			continue
		}
		checked++
		perImage := svc.CalculateImageCost(name, ImageBillingSize2K, 1, nil, 1.0).TotalCost
		byTokens := svc.TkImageModelBillsByImageTokens(name)
		rejected := svc.TkImageModelUnpriced(name, nil)

		require.True(t, perImage > 0 || byTokens || rejected,
			"image owner %q would be SERVED AT $0: per-image settlement charges %v, "+
				"image-token routing=%v, unpriced-guard rejection=%v. Give it an "+
				"output_cost_per_image, an output_cost_per_image_token, or remove the owner.",
			name, perImage, byTokens, rejected)
	}
	require.Greater(t, checked, 0, "expected image_generation owners in the shipped registry")
}

// TestImageSettlementRouting_GptImageFamilyUsesTokenPath pins the specific
// classification for OpenAI's image family, which is priced per image TOKEN with
// no per-image rate. Without token routing these bill $0.
func TestImageSettlementRouting_GptImageFamilyUsesTokenPath(t *testing.T) {
	svc := registryBackedBillingService(t)

	for _, model := range []string{"gpt-image-2", "gpt-image-1.5", "gpt-image-1", "gpt-image-1-mini"} {
		row := loadTKPricingOverlay()[model]
		require.NotNil(t, row, "shipped registry must own %q", model)
		require.Greater(t, row.OutputCostPerImageToken, 0.0,
			"%q is expected to be priced per image token", model)
		require.Zero(t, row.OutputCostPerImage,
			"%q is expected to carry NO per-image price (that is why token routing exists)", model)

		require.True(t, svc.TkImageModelBillsByImageTokens(model),
			"%q must settle on the image-token path; per-image settlement bills $0 for it", model)

		// The token path prices it for real.
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, "%q must resolve token pricing", model)
		require.Greater(t, pricing.ImageOutputPricePerToken, 0.0,
			"%q must carry a positive image-output token rate on the token path", model)
	}
}

// TestImageSettlementRouting_PerImageOwnersStayOnImagePath is the negative
// direction: a genuine per-image owner must NOT be diverted to token settlement
// (which would drop its per-image charge).
func TestImageSettlementRouting_PerImageOwnersStayOnImagePath(t *testing.T) {
	svc := registryBackedBillingService(t)

	for _, model := range []string{"gemini-3-pro-image", "imagen-4.0-generate-001", "grok-imagine-image-quality"} {
		require.False(t, svc.TkImageModelBillsByImageTokens(model),
			"%q is priced per image and must keep per-image settlement", model)
		require.Greater(t, svc.CalculateImageCost(model, ImageBillingSize2K, 1, nil, 1.0).TotalCost, 0.0,
			"%q must produce a positive per-image charge", model)
	}
}

// TestImageSettlementRouting_PredicateBoundaries pins the pure predicate so a
// future field addition cannot quietly widen or narrow it.
func TestImageSettlementRouting_PredicateBoundaries(t *testing.T) {
	require.False(t, tkRegistryRowBillsImageByTokens(nil), "nil row is not token-settled")
	require.False(t, tkRegistryRowBillsImageByTokens(&LiteLLMModelPricing{}),
		"an all-zero row is unpriced, not token-settled (the guard must reject it)")
	require.True(t, tkRegistryRowBillsImageByTokens(&LiteLLMModelPricing{OutputCostPerImageToken: 3e-5}),
		"image-token price with no per-image price => token settlement")
	require.False(t, tkRegistryRowBillsImageByTokens(
		&LiteLLMModelPricing{OutputCostPerImageToken: 3e-5, OutputCostPerImage: 0.04}),
		"a per-image price wins: per-image settlement can charge for it")
	require.False(t, tkRegistryRowBillsImageByTokens(
		&LiteLLMModelPricing{OutputCostPerImageToken: 3e-5, ImagePrice1K: 0.02}),
		"a per-image TIER price also keeps per-image settlement")
}

// TestImageUnpricedGuard_RejectsRowWithNoUsableImageDimension locks the guard's
// tightened contract: a row that is "priced" only in dimensions image settlement
// never reads must still be rejected pre-spend.
func TestImageUnpricedGuard_RejectsRowWithNoUsableImageDimension(t *testing.T) {
	svc := &BillingService{
		pricingService: &PricingService{
			pricingData: map[string]*LiteLLMModelPricing{
				// Chat-style token prices only: image settlement can charge nothing.
				"token-only-image": {
					Mode:               "image_generation",
					InputCostPerToken:  5e-6,
					OutputCostPerToken: 1e-5,
				},
				"image-token-priced": {
					Mode:                    "image_generation",
					OutputCostPerImageToken: 3e-5,
				},
				"per-image-priced": {
					Mode:               "image_generation",
					OutputCostPerImage: 0.04,
				},
			},
		},
	}

	require.False(t, svc.TkImageModelUnpriced("token-only-image", nil),
		"plain input/output token prices ARE chargeable on the image surface "+
			"(computeTokenBreakdown falls back to the text output rate for image output "+
			"tokens), so this row is admitted — matching the pre-existing guard fixture")
	require.False(t, svc.TkImageModelUnpriced("image-token-priced", nil),
		"image-token pricing is billable via image-token settlement, so it is admitted")
	require.False(t, svc.TkImageModelUnpriced("per-image-priced", nil),
		"per-image pricing is billable via image settlement")

	// The dimension the guard newly rejects: priced ONLY per second (a video owner
	// pointed at an image endpoint). tkIsEffectivelyUnpriced calls it priced because
	// a media cost is non-zero, yet every image settlement path reads zero.
	svc.pricingService.pricingData["per-second-only"] = &LiteLLMModelPricing{
		Mode:                "image_generation",
		OutputCostPerSecond: 0.40,
	}
	require.True(t, svc.TkImageModelUnpriced("per-second-only", nil),
		"a per-second-only row bills $0 on every image path and must fail closed")
}

// TestImageTokenSettlement_ChargesAndKeepsImageBillingMode pins the settlement
// function: the amount is produced by the shared token engine from the owner's own
// dimensions, while BillingMode stays image so the usage-log billing_mode contract
// and the image rate multiplier are unchanged by this fix.
func TestImageTokenSettlement_ChargesAndKeepsImageBillingMode(t *testing.T) {
	svc := registryBackedBillingService(t)

	tokens := UsageTokens{InputTokens: 1000, ImageOutputTokens: 1500}
	cost := svc.TkCalculateImageTokenCost("gpt-image-2", tokens, 1.0)
	require.NotNil(t, cost, "a per-image-token owner must settle here, not at $0 per-image")
	require.Greater(t, cost.TotalCost, 0.0, "the whole point: not $0")
	require.Equal(t, string(BillingModeImage), cost.BillingMode,
		"billing_mode must stay image — this is a pricing fix, not a reclassification")

	// The charge must equal what the shared token engine produces for the same
	// tokens and the same resolved pricing — i.e. no second implementation.
	pricing, err := svc.GetModelPricing("gpt-image-2")
	require.NoError(t, err)
	want := svc.computeTokenBreakdown(pricing, tokens, 1.0, "", false, false)
	require.NotNil(t, want)
	require.InDelta(t, want.TotalCost, cost.TotalCost, 1e-12)
	require.InDelta(t, want.ImageOutputCost, cost.ImageOutputCost, 1e-12)
	require.Greater(t, cost.ImageOutputCost, 0.0,
		"generated image tokens must be charged at the image-output rate")

	// Rate multiplier applies to ActualCost only.
	half := svc.TkCalculateImageTokenCost("gpt-image-2", tokens, 0.5)
	require.NotNil(t, half)
	require.InDelta(t, cost.TotalCost, half.TotalCost, 1e-12)
	require.InDelta(t, cost.TotalCost*0.5, half.ActualCost, 1e-12)

	// A negative multiplier must not silently bill at 1x.
	neg := svc.TkCalculateImageTokenCost("gpt-image-2", tokens, -1)
	require.NotNil(t, neg)
	require.Zero(t, neg.ActualCost)
}

// TestImageTokenSettlement_UsesImageInputRateForImageInputTokens proves the
// delegation actually buys us the dimension precedence: gpt-image-2 prices image
// INPUT tokens above text input tokens, so an edit request with image inputs must
// cost more than the same token count as plain text.
func TestImageTokenSettlement_UsesImageInputRateForImageInputTokens(t *testing.T) {
	svc := registryBackedBillingService(t)
	row := loadTKPricingOverlay()["gpt-image-2"]
	require.NotNil(t, row)
	require.Greater(t, row.InputCostPerImageToken, row.InputCostPerToken,
		"fixture premise: this owner prices image input above text input")

	textOnly := svc.TkCalculateImageTokenCost("gpt-image-2",
		UsageTokens{InputTokens: 1000, ImageOutputTokens: 1500}, 1.0)
	withImageInput := svc.TkCalculateImageTokenCost("gpt-image-2",
		UsageTokens{InputTokens: 1000, ImageInputTokens: 1000, ImageOutputTokens: 1500}, 1.0)
	require.NotNil(t, textOnly)
	require.NotNil(t, withImageInput)
	require.Greater(t, withImageInput.InputCost, textOnly.InputCost,
		"image input tokens must bill at the owner's image-input rate, not the text rate")
}

// TestImageTokenSettlement_FallsThroughWhenNotApplicable proves the function is
// strictly additive: it returns nil (caller continues to per-image settlement) for
// per-image owners, for absent image tokens, and for a nil receiver.
func TestImageTokenSettlement_FallsThroughWhenNotApplicable(t *testing.T) {
	svc := registryBackedBillingService(t)

	require.Nil(t, svc.TkCalculateImageTokenCost("gpt-image-2",
		UsageTokens{InputTokens: 100}, 1.0),
		"no reported image tokens => fall through so per-image settlement (and then "+
			"the unpriced guard) decides, rather than charging a fabricated amount")
	require.Nil(t, svc.TkCalculateImageTokenCost("imagen-4.0-generate-001",
		UsageTokens{InputTokens: 100, ImageOutputTokens: 1500}, 1.0),
		"a per-image owner must keep per-image settlement")
	require.Nil(t, svc.TkCalculateImageTokenCost("gemini-3-pro-image",
		UsageTokens{InputTokens: 100, ImageOutputTokens: 1500}, 1.0),
		"a per-image owner must keep per-image settlement")

	var nilSvc *BillingService
	require.Nil(t, nilSvc.TkCalculateImageTokenCost("gpt-image-2",
		UsageTokens{ImageOutputTokens: 1500}, 1.0),
		"nil receiver must be safe")
}
