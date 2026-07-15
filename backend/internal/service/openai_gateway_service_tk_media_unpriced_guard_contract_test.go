//go:build unit

package service

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// Overwrite-protection gates for the media unpriced-reject guard. Both
// premises below can drift SILENTLY (zero compile errors) under future
// refactors or upstream merges; these tests turn the drift into a red
// test-unit job.

// TestMediaUnpricedGuard_ParityWithVideoBilling locks the guard's core
// premise: video billing reads OutputCostPerSecond and nothing else, so
// "guard rejects" must equal "billing would charge $0". If CalculateVideoCost
// ever starts consulting other fields (tiers, token prices, channel data),
// some row below breaks the equality and forces the guard to be updated in
// the same change — instead of silently rejecting now-billable models or
// admitting now-free ones.
func TestMediaUnpricedGuard_ParityWithVideoBilling(t *testing.T) {
	svc := &BillingService{
		pricingService: &PricingService{
			pricingData: map[string]*LiteLLMModelPricing{
				"per-second-priced": {OutputCostPerSecond: 0.40, Mode: "video_generation"},
				"token-only-priced": {InputCostPerToken: 1e-6, OutputCostPerToken: 4e-5, Mode: "chat"},
				"per-image-only":    {OutputCostPerImage: 0.04, Mode: "image_generation"},
				"zero-placeholder":  {Mode: "video_generation"},
			},
		},
	}
	for _, model := range []string{
		"per-second-priced",
		"token-only-priced",
		"per-image-only",
		"zero-placeholder",
		"absent-model",
	} {
		guardRejects := svc.TkVideoModelUnpriced(model)
		billsZero := svc.CalculateVideoCost(model, VideoBillingResolution720P, 1, 8, nil, 1.0).TotalCost <= 0
		require.Equal(t, billsZero, guardRejects,
			"guard/billing parity broken for %q: guard rejects=%v but bills zero=%v — "+
				"video billing semantics changed; update TkVideoModelUnpriced in the same change",
			model, guardRejects, billsZero)
	}
}

// TestMediaUnpricedGuard_EmptyModelDefaultIsPriced locks the fail-open
// premise for model-less image requests: the guard lets them through because
// the OAuth forward layer defaults the model — which is only safe while that
// DEFAULT is itself a priced model. The default name is extracted from the
// live source (same package), so an upstream merge that changes it to an
// unpriced model turns this red instead of silently re-opening the
// unpriced-media hole; the shipped fallback pricing + overlay is the same
// chain the runtime resolves through.
func TestMediaUnpricedGuard_EmptyModelDefaultIsPriced(t *testing.T) {
	src, err := os.ReadFile("openai_images_responses.go")
	require.NoError(t, err)
	m := regexp.MustCompile(`requestModel = "([^"]+)"`).FindSubmatch(src)
	require.NotNil(t, m,
		"model-less image defaulting (requestModel = \"...\") not found in openai_images_responses.go — "+
			"if defaulting moved or was removed, re-evaluate the empty-model fail-open in TkImageModelUnpriced")
	defaultModel := string(m[1])

	fallback, err := os.ReadFile("../../resources/model-pricing/model_prices_and_context_window.json")
	require.NoError(t, err)
	data, err := (&PricingService{}).parsePricingData(fallback)
	require.NoError(t, err)
	svc := &BillingService{pricingService: &PricingService{pricingData: data}}

	require.False(t, svc.TkImageModelUnpriced(defaultModel, nil),
		"the model-less image default %q must be priced in the shipped pricing chain — "+
			"an unpriced default makes the guard's empty-model fail-open a billing hole",
		defaultModel)
}
