//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// overlayResolver builds a resolver whose base pricing comes from the embedded TK
// overlay (a minimal source body triggers the always-on overlay fill), with no
// channel service — so the overlay-interval path is what populates Intervals.
func overlayResolver(t *testing.T) *ModelPricingResolver {
	t.Helper()
	ps := &PricingService{}
	data, err := ps.parsePricingData([]byte(`{"gpt-5.4":{"input_cost_per_token":5e-7,"output_cost_per_token":2e-6,"litellm_provider":"openai","mode":"chat"}}`))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	return NewModelPricingResolver(nil, billing)
}

// TestOverlayLoader_ParsesIntervals pins the loader: qwen3-coder-plus carries its
// 4 input-token tiers with the right boundaries (min exclusive / max inclusive,
// nil = unbounded top tier) and USD-per-token prices.
func TestOverlayLoader_ParsesIntervals(t *testing.T) {
	overlay := loadTKPricingOverlay()

	coder := overlay["qwen3-coder-plus"]
	require.NotNil(t, coder, "overlay must carry qwen3-coder-plus")
	require.Len(t, coder.Intervals, 4)
	require.Equal(t, 0, coder.Intervals[0].MinTokens)
	require.NotNil(t, coder.Intervals[0].MaxTokens)
	require.Equal(t, 32000, *coder.Intervals[0].MaxTokens)
	require.NotNil(t, coder.Intervals[0].InputPrice)
	require.InDelta(t, 5.74e-07, *coder.Intervals[0].InputPrice, 1e-15)
	require.Nil(t, coder.Intervals[3].MaxTokens, "top tier must be unbounded (>256K)")
	require.NotNil(t, coder.Intervals[3].OutputPrice)
	require.InDelta(t, 2.8671e-05, *coder.Intervals[3].OutputPrice, 1e-15)

	// flat base is the out-of-range fallback = tier 1.
	require.InDelta(t, 5.74e-07, coder.InputCostPerToken, 1e-15)
	require.False(t, tkIsEffectivelyUnpriced(coder))
}

// TestOverlayIntervalPricing_CoderPlusWholeRequestTier verifies the end-to-end
// resolver behaviour: overlay intervals land on ResolvedPricing.Intervals (no
// channel), and GetIntervalPricing selects the whole-request tier by input-context
// tokens — matching DashScope's "0<Token<=32K / ... / 256K<Token<=1M" model.
func TestOverlayIntervalPricing_CoderPlusWholeRequestTier(t *testing.T) {
	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "qwen3-coder-plus"})
	require.NotNil(t, resolved.BasePricing)
	require.Len(t, resolved.Intervals, 4, "overlay intervals must populate ResolvedPricing.Intervals when no channel")
	require.Equal(t, PricingSourceLiteLLM, resolved.Source)

	cases := []struct {
		ctxTokens int
		in, out   float64
	}{
		{10_000, 5.74e-07, 2.294e-06},   // tier1 (0,32K]
		{32_000, 5.74e-07, 2.294e-06},   // 32K boundary stays tier1 (max inclusive)
		{32_001, 8.61e-07, 3.441e-06},   // tier2 (32K,128K]
		{200_000, 1.434e-06, 5.735e-06}, // tier3 (128K,256K]
		{500_000, 2.868e-06, 2.8671e-05}, // tier4 (256K,inf)
	}
	for _, c := range cases {
		p := r.GetIntervalPricing(resolved, c.ctxTokens)
		require.NotNilf(t, p, "interval pricing @ %d ctx", c.ctxTokens)
		require.InDeltaf(t, c.in, p.InputPricePerToken, 1e-15, "input @ %d ctx", c.ctxTokens)
		require.InDeltaf(t, c.out, p.OutputPricePerToken, 1e-15, "output @ %d ctx", c.ctxTokens)
	}
}

// TestOverlayIntervalPricing_PlusFlashTwoTier covers the 2-tier (256K) models.
func TestOverlayIntervalPricing_PlusFlashTwoTier(t *testing.T) {
	r := overlayResolver(t)

	plus := r.Resolve(context.Background(), PricingInput{Model: "qwen3.7-plus"})
	require.Len(t, plus.Intervals, 2)
	require.InDelta(t, 2.76e-07, r.GetIntervalPricing(plus, 100_000).InputPricePerToken, 1e-15)
	require.InDelta(t, 8.26e-07, r.GetIntervalPricing(plus, 300_000).InputPricePerToken, 1e-15)
	require.InDelta(t, 3.301e-06, r.GetIntervalPricing(plus, 300_000).OutputPricePerToken, 1e-15)

	flash := r.Resolve(context.Background(), PricingInput{Model: "qwen3.6-flash"})
	require.Len(t, flash.Intervals, 2)
	require.InDelta(t, 1.65e-07, r.GetIntervalPricing(flash, 100_000).InputPricePerToken, 1e-15)
	require.InDelta(t, 3.961e-06, r.GetIntervalPricing(flash, 300_000).OutputPricePerToken, 1e-15)
}

// TestOverlayIntervals_FlatModelUnaffected guards the orthogonality: a flat overlay
// model (qwen3.7-max) carries no intervals, so it resolves to plain flat pricing.
func TestOverlayIntervals_FlatModelUnaffected(t *testing.T) {
	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "qwen3.7-max"})
	require.Empty(t, resolved.Intervals, "flat overlay model must not gain intervals")
	require.NotNil(t, resolved.BasePricing)
	require.InDelta(t, 1.65e-06, resolved.BasePricing.InputPricePerToken, 1e-15)
}
