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
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), *coder.Intervals[0].InputPrice, 1e-15)
	require.Nil(t, coder.Intervals[3].MaxTokens, "top tier must be unbounded (>256K)")
	require.NotNil(t, coder.Intervals[3].OutputPrice)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(200), *coder.Intervals[3].OutputPrice, 1e-15)

	// flat base is the out-of-range fallback = tier 1.
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), coder.InputCostPerToken, 1e-15)
	require.False(t, tkIsEffectivelyUnpriced(coder))

	// VolcEngine doubao-seed-2.0-pro: 3 input tiers with per-tier cache-hit price
	// (official ¥/M ÷ 6.7): [0,32K] 3.2/0.64/16, (32,128K] 4.8/0.96/24, (128K,∞] 9.6/1.92/48.
	pro := overlay["doubao-seed-2-0-pro-260215"]
	require.NotNil(t, pro)
	require.Len(t, pro.Intervals, 3)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(3.2), *pro.Intervals[0].InputPrice, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.64), *pro.Intervals[0].CacheReadPrice, 1e-15, "per-tier cache-hit price, not $0")
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(9.6), *pro.Intervals[2].InputPrice, 1e-15, "top tier (128K+) input")
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(48.0), *pro.Intervals[2].OutputPrice, 1e-15, "top tier output")
	require.Nil(t, pro.Intervals[2].MaxTokens, "top tier unbounded")

	// BigModel GLM: overlay stores pre-tax RMB÷6.7 prices. GLM-4.7 has an
	// output-length subtier on the official page; because TK intervals key only
	// by input tokens, the first interval pins the higher reachable 0-32K row.
	glm47 := overlay["glm-4.7"]
	require.NotNil(t, glm47)
	require.Len(t, glm47.Intervals, 2)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(3), *glm47.Intervals[0].InputPrice, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(14), *glm47.Intervals[0].OutputPrice, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.6), *glm47.Intervals[0].CacheReadPrice, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4), *glm47.Intervals[1].InputPrice, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(16), *glm47.Intervals[1].OutputPrice, 1e-15)
	require.Nil(t, glm47.Intervals[1].MaxTokens)
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

	tax := tkOfficialListBaseTaxMultiplier()
	withTax := func(cny float64) float64 {
		return tax * tkCNYPerMTokToUSDPerToken(cny)
	}
	cases := []struct {
		ctxTokens int
		in, out   float64
	}{
		{10_000, withTax(4), withTax(16)},    // tier1 (0,32K] ¥4/¥16
		{32_000, withTax(4), withTax(16)},    // 32K boundary stays tier1 (max inclusive)
		{32_001, withTax(6), withTax(24)},    // tier2 (32K,128K] ¥6/¥24
		{200_000, withTax(10), withTax(40)},  // tier3 (128K,256K] ¥10/¥40
		{500_000, withTax(20), withTax(200)}, // tier4 (256K,inf) ¥20/¥200
	}
	for _, c := range cases {
		p := r.GetIntervalPricing(resolved, c.ctxTokens)
		require.NotNilf(t, p, "interval pricing @ %d ctx", c.ctxTokens)
		require.InDeltaf(t, c.in, p.InputPricePerToken, 1e-15, "input @ %d ctx", c.ctxTokens)
		require.InDeltaf(t, c.out, p.OutputPricePerToken, 1e-15, "output @ %d ctx", c.ctxTokens)
		// Cache-read tokens must bill at the tier's full input rate ("用原价", no
		// discount) — never $0. DashScope reports cached_tokens and the bridge maps
		// them to CacheReadTokens (newapi_bridge_usage.go), so an unset cache price
		// would silently bill cache hits free.
		require.InDeltaf(t, c.in, p.CacheReadPricePerToken, 1e-15, "cache-read @ %d ctx must equal input rate", c.ctxTokens)
	}
}

// TestOverlayIntervalPricing_PlusFlashTwoTier covers the 2-tier (256K) models.
func TestOverlayIntervalPricing_PlusFlashTwoTier(t *testing.T) {
	r := overlayResolver(t)

	plus := r.Resolve(context.Background(), PricingInput{Model: "qwen3.7-plus"})
	require.Len(t, plus.Intervals, 2)
	tax := tkOfficialListBaseTaxMultiplier()
	withTax := func(cny float64) float64 {
		return tax * tkCNYPerMTokToUSDPerToken(cny)
	}
	require.InDelta(t, withTax(2), r.GetIntervalPricing(plus, 100_000).InputPricePerToken, 1e-15)   // tier1 ¥2
	require.InDelta(t, withTax(6), r.GetIntervalPricing(plus, 300_000).InputPricePerToken, 1e-15)   // tier2 ¥6
	require.InDelta(t, withTax(24), r.GetIntervalPricing(plus, 300_000).OutputPricePerToken, 1e-15) // tier2 ¥24

	flash := r.Resolve(context.Background(), PricingInput{Model: "qwen3.6-flash"})
	require.Len(t, flash.Intervals, 2)
	require.InDelta(t, withTax(1.2), r.GetIntervalPricing(flash, 100_000).InputPricePerToken, 1e-15)   // tier1 ¥1.2
	require.InDelta(t, withTax(28.8), r.GetIntervalPricing(flash, 300_000).OutputPricePerToken, 1e-15) // tier2 ¥28.8
}

func TestOverlayIntervalPricing_GLMUsesBigModelTiersAndBaseTax(t *testing.T) {
	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "glm-5.1"})
	require.NotNil(t, resolved.BasePricing)
	require.Len(t, resolved.Intervals, 2)

	tax := tkOfficialListBaseTaxMultiplier()
	withTax := func(cny float64) float64 {
		return tax * tkCNYPerMTokToUSDPerToken(cny)
	}
	require.InDelta(t, withTax(6), r.GetIntervalPricing(resolved, 10_000).InputPricePerToken, 1e-15)
	require.InDelta(t, withTax(24), r.GetIntervalPricing(resolved, 10_000).OutputPricePerToken, 1e-15)
	require.InDelta(t, withTax(1.3), r.GetIntervalPricing(resolved, 10_000).CacheReadPricePerToken, 1e-15)
	require.InDelta(t, withTax(8), r.GetIntervalPricing(resolved, 40_000).InputPricePerToken, 1e-15)
	require.InDelta(t, withTax(28), r.GetIntervalPricing(resolved, 40_000).OutputPricePerToken, 1e-15)
	require.InDelta(t, withTax(2), r.GetIntervalPricing(resolved, 40_000).CacheReadPricePerToken, 1e-15)
}

// TestOverlayIntervals_FlatModelUnaffected guards the orthogonality: a flat overlay
// model (qwen3.7-max) carries no intervals, so it resolves to plain flat pricing.
func TestOverlayIntervals_FlatModelUnaffected(t *testing.T) {
	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "qwen3.7-max"})
	require.Empty(t, resolved.Intervals, "flat overlay model must not gain intervals")
	require.NotNil(t, resolved.BasePricing)
	tax := tkOfficialListBaseTaxMultiplier()
	withTax := func(cny float64) float64 {
		return tax * tkCNYPerMTokToUSDPerToken(cny)
	}
	require.InDelta(t, withTax(12), resolved.BasePricing.InputPricePerToken, 1e-15)
	// cache-read billed at full input rate ("用原价"), not $0.
	require.InDelta(t, withTax(12), resolved.BasePricing.CacheReadPricePerToken, 1e-15)
}
