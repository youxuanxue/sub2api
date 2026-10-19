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

// overlayIntervalBoundarySamples derives ctx-token probes from overlay interval
// boundaries (inclusive max, just above max, mid first tier, deep top tier).
// No handwritten ¥ rates — only structural samples from the SSOT row.
func overlayIntervalBoundarySamples(intervals []PricingInterval) []int {
	seen := make(map[int]struct{})
	var out []int
	add := func(n int) {
		if n < 0 {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for _, iv := range intervals {
		if iv.MinTokens == 0 {
			add(10_000)
		}
		if iv.MaxTokens != nil {
			add(*iv.MaxTokens)
			add(*iv.MaxTokens + 1)
		} else {
			add(iv.MinTokens + 100_000)
		}
	}
	return out
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
// tokens. Expected rates are derived from overlay SSOT × base tax — not a
// handwritten ¥/Mtok table.
func TestOverlayIntervalPricing_CoderPlusWholeRequestTier(t *testing.T) {
	const modelID = "qwen3-coder-plus"
	overlayRow := loadTKPricingOverlay()[modelID]
	require.NotNil(t, overlayRow, modelID)
	require.Len(t, overlayRow.Intervals, 4)

	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: modelID})
	require.NotNil(t, resolved.BasePricing)
	require.Len(t, resolved.Intervals, len(overlayRow.Intervals),
		"overlay intervals must populate ResolvedPricing.Intervals when no channel")
	require.Equal(t, PricingSourceLiteLLM, resolved.Source)

	tax := tkOfficialListBaseTaxMultiplier()
	ctxSamples := overlayIntervalBoundarySamples(overlayRow.Intervals)
	require.NotEmpty(t, ctxSamples)
	for _, ctx := range ctxSamples {
		want := FindMatchingInterval(overlayRow.Intervals, ctx)
		require.NotNilf(t, want, "overlay interval @ %d ctx", ctx)
		require.NotNil(t, want.InputPrice)
		require.NotNil(t, want.OutputPrice)
		require.NotNil(t, want.CacheReadPrice, "tier cache_read must be set in overlay SSOT")

		p := r.GetIntervalPricing(resolved, ctx)
		require.NotNilf(t, p, "interval pricing @ %d ctx", ctx)
		require.InDeltaf(t, tax**want.InputPrice, p.InputPricePerToken, 1e-15, "input @ %d ctx", ctx)
		require.InDeltaf(t, tax**want.OutputPrice, p.OutputPricePerToken, 1e-15, "output @ %d ctx", ctx)
		require.InDeltaf(t, tax**want.CacheReadPrice, p.CacheReadPricePerToken, 1e-15,
			"cache-read @ %d ctx must match overlay tier (never $0 / full input)", ctx)
		require.Less(t, p.CacheReadPricePerToken, p.InputPricePerToken,
			"cache-read @ %d ctx must stay below input", ctx)
	}
}

// TestOverlayIntervalPricing_PlusFlashTwoTier covers the 2-tier (256K) models.
// Rates come from overlay SSOT; ctx samples are interval boundaries only.
func TestOverlayIntervalPricing_PlusFlashTwoTier(t *testing.T) {
	r := overlayResolver(t)
	tax := tkOfficialListBaseTaxMultiplier()

	assertOverlayIntervalResolve := func(modelID string) {
		t.Helper()
		overlayRow := loadTKPricingOverlay()[modelID]
		require.NotNil(t, overlayRow, modelID)
		require.Len(t, overlayRow.Intervals, 2, modelID)
		resolved := r.Resolve(context.Background(), PricingInput{Model: modelID})
		require.Len(t, resolved.Intervals, 2, modelID)
		for _, ctx := range overlayIntervalBoundarySamples(overlayRow.Intervals) {
			want := FindMatchingInterval(overlayRow.Intervals, ctx)
			require.NotNilf(t, want, "%s overlay interval @ %d", modelID, ctx)
			p := r.GetIntervalPricing(resolved, ctx)
			require.NotNilf(t, p, "%s interval pricing @ %d", modelID, ctx)
			require.InDeltaf(t, tax**want.InputPrice, p.InputPricePerToken, 1e-15, "%s input @ %d", modelID, ctx)
			require.InDeltaf(t, tax**want.OutputPrice, p.OutputPricePerToken, 1e-15, "%s output @ %d", modelID, ctx)
		}
	}
	assertOverlayIntervalResolve("qwen3.7-plus")
	assertOverlayIntervalResolve("qwen3.6-flash")
}

func TestOverlayIntervalPricing_GLM51UsesMigrationBaselineFlatPriceAndBaseTax(t *testing.T) {
	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: "glm-5.1"})
	require.NotNil(t, resolved.BasePricing)
	require.Empty(t, resolved.Intervals,
		"migration must not promote previously ineffective overlay tiers")

	tax := tkOfficialListBaseTaxMultiplier()
	for _, inputTokens := range []int{10_000, 40_000} {
		pricing := r.GetIntervalPricing(resolved, inputTokens)
		require.InDelta(t, tax*1.4e-6, pricing.InputPricePerToken, 1e-15)
		require.InDelta(t, tax*4.4e-6, pricing.OutputPricePerToken, 1e-15)
		require.InDelta(t, tax*2.8e-7, pricing.CacheReadPricePerToken, 1e-15)
	}
}

// TestOverlayIntervals_FlatModelUnaffected guards the orthogonality: a flat overlay
// model (qwen3.7-max) carries no intervals, so it resolves to plain flat pricing.
// Expected rates are derived from overlay SSOT × base tax.
func TestOverlayIntervals_FlatModelUnaffected(t *testing.T) {
	const modelID = "qwen3.7-max"
	overlayRow := loadTKPricingOverlay()[modelID]
	require.NotNil(t, overlayRow, modelID)
	require.Empty(t, overlayRow.Intervals, "fixture must be a flat overlay row")
	require.Greater(t, overlayRow.CacheReadInputTokenCost, 0.0)
	require.Less(t, overlayRow.CacheReadInputTokenCost, overlayRow.InputCostPerToken)

	r := overlayResolver(t)
	resolved := r.Resolve(context.Background(), PricingInput{Model: modelID})
	require.Empty(t, resolved.Intervals, "flat overlay model must not gain intervals")
	require.NotNil(t, resolved.BasePricing)
	tax := tkOfficialListBaseTaxMultiplier()
	require.InDelta(t, tax*overlayRow.InputCostPerToken, resolved.BasePricing.InputPricePerToken, 1e-15)
	require.InDelta(t, tax*overlayRow.CacheReadInputTokenCost, resolved.BasePricing.CacheReadPricePerToken, 1e-15)
	require.Less(t, resolved.BasePricing.CacheReadPricePerToken, resolved.BasePricing.InputPricePerToken,
		"cache_read must stay below input (not $0 and not full input)")
}

func TestOverlayIntervals_ChannelFlatOverrideSuppressesRegistryTiers(t *testing.T) {
	resolver := NewModelPricingResolver(nil, &BillingService{})
	registryInput := 2e-6
	registryOutput := 8e-6
	channelInput := 11e-6
	channelOutput := 44e-6
	resolved := &ResolvedPricing{
		Mode: BillingModeToken,
		BasePricing: &ModelPricing{
			InputPricePerToken:  registryInput,
			OutputPricePerToken: registryOutput,
			Intervals: []PricingInterval{{
				MinTokens:   0,
				MaxTokens:   testPtrInt(128_000),
				InputPrice:  &registryInput,
				OutputPrice: &registryOutput,
			}},
		},
	}

	resolver.applyTokenOverrides(&ChannelModelPricing{
		BillingMode: BillingModeToken,
		InputPrice:  &channelInput,
		OutputPrice: &channelOutput,
	}, resolved)
	tkApplyOverlayIntervals(resolved)

	require.Empty(t, resolved.Intervals, "channel flat pricing must suppress lower-priority registry intervals")
	inRange := resolver.GetIntervalPricing(resolved, 32_000)
	require.InDelta(t, channelInput, inRange.InputPricePerToken, 1e-15)
	require.InDelta(t, channelOutput, inRange.OutputPricePerToken, 1e-15)
	outOfRange := resolver.GetIntervalPricing(resolved, 256_000)
	require.InDelta(t, channelInput, outOfRange.InputPricePerToken, 1e-15)
	require.InDelta(t, channelOutput, outOfRange.OutputPricePerToken, 1e-15)
}

func TestOverlayIntervals_ChannelPartialFlatOverrideKeepsUnconfiguredRegistryTierDimensions(t *testing.T) {
	resolver := NewModelPricingResolver(nil, &BillingService{})
	registryInput := 2e-6
	registryOutput := 8e-6
	channelInput := 11e-6
	resolved := &ResolvedPricing{
		Mode: BillingModeToken,
		BasePricing: &ModelPricing{
			InputPricePerToken:  registryInput,
			OutputPricePerToken: registryOutput,
			Intervals: []PricingInterval{{
				MinTokens:   0,
				MaxTokens:   testPtrInt(128_000),
				InputPrice:  &registryInput,
				OutputPrice: &registryOutput,
			}},
		},
	}

	resolver.applyTokenOverrides(&ChannelModelPricing{
		BillingMode: BillingModeToken,
		InputPrice:  &channelInput,
	}, resolved)
	tkApplyOverlayIntervals(resolved)

	require.Len(t, resolved.Intervals, 1, "unconfigured output must retain its registry tier")
	inRange := resolver.GetIntervalPricing(resolved, 32_000)
	require.InDelta(t, channelInput, inRange.InputPricePerToken, 1e-15)
	require.InDelta(t, registryOutput, inRange.OutputPricePerToken, 1e-15)
}
