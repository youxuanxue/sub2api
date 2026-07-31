//go:build unit

package service

import (
	"testing"
)

// TestOverlayHasNoTieredAndPeakValleyModel is a mechanical guard on the
// assumption the /pricing + /models variant rendering is built on: a model
// carries EITHER an interval (阶梯) ladder OR time-of-day (峰谷) pricing, never
// both.
//
// The frontend owner module (frontend/src/utils/pricingVariants.tk.ts) renders
// one labelled price line per bracket, or one per peak/off-peak side. A model
// with both would need the 3×2 cross-product, which the current shape cannot
// express — it silently drops the peak dimension and shows only the ladder.
//
// If a future overlay edit introduces such a model, fail here (where the fix is
// cheap and obvious) rather than shipping a page that under-states the price
// during peak windows. The fix is to extend the frontend variant shape first.
//
// Model sets are derived from the overlay + policy, per CLAUDE.md: no
// hand-maintained model lists.
func TestOverlayHasNoTieredAndPeakValleyModel(t *testing.T) {
	overlay := loadTKPricingOverlay()
	if len(overlay) == 0 {
		t.Fatal("overlay is empty — the embedded tk_pricing_overlay.json should always load")
	}

	var tiered, peak, thinking int
	for modelID, pricing := range overlay {
		if pricing == nil {
			continue
		}
		hasTiers := len(pricing.Intervals) > 1
		hasPeak := tkDeepSeekPeakValleyApplies(modelID, PricingSourceRegistry)
		hasThinking := pricing.ThinkingOutputCostPerToken > 0
		if hasTiers {
			tiered++
		}
		if hasPeak {
			peak++
		}
		if hasThinking {
			thinking++
		}
		if hasTiers && hasPeak {
			t.Errorf("model %q has BOTH an interval ladder (%d brackets) and peak-valley pricing; "+
				"the pricing catalog UI renders one variant dimension per model and would drop the peak "+
				"dimension. Extend frontend/src/utils/pricingVariants.tk.ts before adding such a model.",
				modelID, len(pricing.Intervals))
		}
		// Thinking premium is rendered as a sub-line of the FLAT output price
		// (PricingView.vue: the `v-if="row.thinkingOutputPer1K"` block lives inside
		// the flat `v-else-if` branch, which a variant row never reaches). So a
		// model that is variant-priced AND carries a thinking premium would show
		// its per-bracket prices while silently dropping the thinking rate — the
		// same class of under-statement this whole change set exists to remove.
		// No such model exists today; fail here rather than in production.
		if hasThinking && (hasTiers || hasPeak) {
			t.Errorf("model %q has a thinking-output premium AND variant pricing "+
				"(tiered=%v peak=%v); the thinking price renders only in the flat branch and "+
				"would be dropped. Render thinking inside the variant branch of "+
				"frontend/src/utils/pricingVariants.tk.ts + PricingView.vue before adding such a model.",
				modelID, hasTiers, hasPeak)
		}
	}

	// Guard the guard: if a dimension no longer exists in the overlay, the
	// corresponding assertion above passes vacuously and protects nothing.
	if tiered == 0 {
		t.Error("no tiered model found in the overlay — the 阶梯 disclosure path is now untested")
	}
	if peak == 0 {
		t.Error("no peak-valley model found in the overlay — the 峰谷 disclosure path is now untested")
	}
	if thinking == 0 {
		t.Error("no thinking-premium model found in the overlay — the thinking-vs-variant guard is now vacuous")
	}
}

// TestPublicCatalogTieredModelsCarryFullLadder asserts the public catalog
// surfaces every bracket of a tiered model, not just the first. Before the
// ladder was surfaced, the flat fields were the only thing published and the
// page showed a first-tier price as if it were the price — doubao-seed-2-0-pro's
// top bracket is ~3× the first, qwen-plus's top output bracket ~24×.
func TestPublicCatalogTieredModelsCarryFullLadder(t *testing.T) {
	overlay := loadTKPricingOverlay()
	if len(overlay) == 0 {
		t.Fatal("overlay is empty")
	}

	// Positive set derived from the overlay itself.
	want := make(map[string]int)
	for modelID, pricing := range overlay {
		if pricing != nil && len(pricing.Intervals) > 1 {
			want[modelID] = len(pricing.Intervals)
		}
	}
	if len(want) == 0 {
		t.Skip("no tiered models in the overlay")
	}

	resp := &PublicCatalogResponse{Object: "list"}
	for modelID := range want {
		resp.Data = append(resp.Data, PublicCatalogModel{
			ModelID: modelID,
			Pricing: PublicCatalogPricing{Currency: "USD"},
		})
	}
	attachCatalogOverlayTiers(resp)

	for i := range resp.Data {
		modelID := resp.Data[i].ModelID
		tiers := resp.Data[i].Pricing.Tiers
		if got, expected := len(tiers), want[modelID]; got != expected {
			t.Errorf("model %q: catalog published %d tiers, overlay defines %d", modelID, got, expected)
			continue
		}
		// A ladder whose brackets all price the same is a flat price with extra
		// steps — and a sign the per-bracket prices were lost in translation.
		distinct := make(map[float64]struct{}, len(tiers))
		for _, tier := range tiers {
			distinct[tier.InputPer1KTokens] = struct{}{}
		}
		if len(distinct) == 1 && len(tiers) > 1 {
			t.Errorf("model %q: all %d published tiers share one input price — per-bracket prices lost", modelID, len(tiers))
		}
	}
}

// TestPublicCatalogPeakValleyIsFlatTimesMultiplier pins the relationship the UI
// states in words ("off-peak shown; peak = ×N"): the published peak prices must
// be the flat (off-peak) prices scaled by the policy multiplier. If billing and
// disclosure ever diverge here, the page lies about what a peak request costs.
func TestPublicCatalogPeakValleyIsFlatTimesMultiplier(t *testing.T) {
	policy := loadTkDeepSeekPeakValleyPolicy()
	if policy == nil || policy.PeakMultiplier <= 1 {
		t.Skip("no peak-valley policy configured")
	}

	const (
		flatIn        = 0.001
		flatOut       = 0.002
		flatCacheRead = 0.0001
	)

	// Find a model the policy actually matches, from the policy's own matchers.
	var modelID string
	for id, pricing := range loadTKPricingOverlay() {
		if pricing != nil && tkDeepSeekPeakValleyApplies(id, PricingSourceRegistry) {
			modelID = id
			break
		}
	}
	if modelID == "" {
		t.Skip("no peak-priced model in the overlay")
	}

	resp := &PublicCatalogResponse{
		Object: "list",
		Data: []PublicCatalogModel{{
			ModelID: modelID,
			Pricing: PublicCatalogPricing{
				Currency:          "USD",
				InputPer1KTokens:  flatIn,
				OutputPer1KTokens: flatOut,
				CacheReadPer1K:    flatCacheRead,
			},
		}},
	}
	attachCatalogDeepSeekPeakValley(resp)

	pv := resp.Data[0].Pricing.PeakValley
	if pv == nil {
		t.Fatalf("model %q matches the peak policy but no peak_valley block was published", modelID)
	}
	if len(pv.Windows) == 0 {
		t.Error("peak_valley published with no windows — the UI caption would have nothing to explain")
	}
	if pv.Timezone == "" {
		t.Error("peak_valley published with no timezone — windows would be ambiguous to the reader")
	}

	m := policy.PeakMultiplier
	const epsilon = 1e-12
	for _, c := range []struct {
		name      string
		flat, got float64
	}{
		{"input", flatIn, pv.InputPer1KTokens},
		{"output", flatOut, pv.OutputPer1KTokens},
		{"cache_read", flatCacheRead, pv.CacheReadPer1K},
	} {
		if want := c.flat * m; want-c.got > epsilon || c.got-want > epsilon {
			t.Errorf("peak %s: published %v, want flat %v × %v = %v", c.name, c.got, c.flat, m, want)
		}
	}

	// The flat fields must stay the OFF-PEAK price — the UI labels them 谷时.
	if resp.Data[0].Pricing.InputPer1KTokens != flatIn {
		t.Errorf("flat input price mutated to %v; it must remain the off-peak price",
			resp.Data[0].Pricing.InputPer1KTokens)
	}
}
