//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The `.qianfan` suffix owners are gone: user-facing billing is intentionally
// independent of the upstream account that serves the request. Shared client ids
// use the single global owner; provider/account cost differences belong to profit
// reporting, not ActualCost. channel_model_pricing remains a customer commercial
// scope and must not be used as a proxy for a serving-account price.
// See docs/approved/pricing-serving-single-source-of-truth.md §1.
func TestTkQianfanScopedOverlayKeysAreRemoved(t *testing.T) {
	t.Parallel()
	overlay := loadTKPricingOverlay()
	for _, key := range []string{
		"deepseek-v4-pro.qianfan",
		"deepseek-v4-flash.qianfan",
		"glm-5.qianfan",
		"glm-5.1.qianfan",
		"glm-5.2.qianfan",
		"kimi-k2.6.qianfan",
	} {
		require.Nil(t, overlay[key],
			"%s must not exist: shared ids use one global user price regardless of serving account", key)
	}
}

// Fixed 0731 requests keep their historical official price after stable Flash upgrades.
func TestTkQianfanDatedFlashKeepsHistoricalPrice(t *testing.T) {
	t.Parallel()
	svc := newTestBillingService()
	dated, err := svc.GetModelPricing("deepseek-v4-flash-0731")
	require.NoError(t, err)
	tax := tkOfficialListBaseTaxMultiplier()
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1.5)*tax, dated.InputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(4.5)*tax, dated.OutputPricePerToken, 1e-15)
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(0.05)*tax, dated.CacheReadPricePerToken, 1e-15)
	stable, err := svc.GetModelPricing("deepseek-flash")
	require.NoError(t, err)
	require.NotEqual(t, dated.InputPricePerToken, stable.InputPricePerToken)
	require.False(t, svc.IsServedViaFamilyFloor("deepseek-v4-flash-0731"), "a historical direct price must not raise fallback alerts")
}

// Historical official snapshots retain the same peak-window policy.
func TestTkDeepSeekPeakValleyAppliesToDatedFlashSnapshot(t *testing.T) {
	t.Parallel()
	policy := loadTkDeepSeekPeakValleyPolicy()
	require.NotNil(t, policy)
	require.True(t, tkDeepSeekPeakValleyAppliesWithPolicy(policy, "deepseek-v4-flash-0731", PricingSourceLiteLLM))
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	peak := time.Date(2026, 9, 15, 10, 0, 0, 0, shanghai)
	base := &ModelPricing{InputPricePerToken: 0.0015, OutputPricePerToken: 0.0045}
	scaled := tkApplyDeepSeekPeakValleyPricing("deepseek-v4-flash-0731", base, peak, PricingSourceLiteLLM)
	require.InDelta(t, base.InputPricePerToken*policy.PeakMultiplier, scaled.InputPricePerToken, 1e-12)
	require.InDelta(t, base.OutputPricePerToken*policy.PeakMultiplier, scaled.OutputPricePerToken, 1e-12)
}

// Qianfan-ONLY owners (no official DeepSeek equivalent) keep their registry row
// AND keep their peak-valley exemption: Qianfan list pricing has no peak window,
// so applying the official 2x multiplier to them would overbill.
func TestTkQianfanOnlyDeepSeekOwnersStayPeakExempt(t *testing.T) {
	t.Parallel()
	policy := loadTkDeepSeekPeakValleyPolicy()
	require.NotNil(t, policy)
	overlay := loadTKPricingOverlay()

	for _, model := range []string{"deepseek-v3.2", "deepseek-v3.2-think", "deepseek-ocr"} {
		require.NotNil(t, overlay[model], "%s is a Qianfan-only owner and must keep its row", model)
		require.False(t, tkDeepSeekPeakValleyAppliesWithPolicy(policy, model, PricingSourceLiteLLM),
			"%s is priced from the Qianfan list, which has no peak window", model)
	}

	// A real in-window instant (policy timezone, not the process timezone), so
	// "unchanged" proves the exemption rather than merely missing the window.
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	at := time.Date(2026, 7, 21, 10, 0, 0, 0, shanghai)
	require.InDelta(t, policy.PeakMultiplier, tkDeepSeekPeakMultiplierAtWithPolicy(policy, at), 1e-12,
		"fixture instant must be inside a peak window for this exemption test to mean anything")

	base := &ModelPricing{InputPricePerToken: 0.002, OutputPricePerToken: 0.003}
	scaled := tkApplyDeepSeekPeakValleyPricing("deepseek-v3.2", base, at, PricingSourceLiteLLM)
	require.InDelta(t, base.InputPricePerToken, scaled.InputPricePerToken, 1e-12)
	require.InDelta(t, base.OutputPricePerToken, scaled.OutputPricePerToken, 1e-12)
}

func TestTkQianfanOverlay_DeepSeekV32UsesTieredIntervals(t *testing.T) {
	t.Parallel()
	entry := loadTKPricingOverlay()["deepseek-v3.2"]
	require.NotNil(t, entry)
	require.Len(t, entry.Intervals, 2)
	require.NotNil(t, entry.Intervals[0].MaxTokens)
	require.Equal(t, 32000, *entry.Intervals[0].MaxTokens)
	require.Nil(t, entry.Intervals[1].MaxTokens)
	require.InDelta(t, 2.9850746268656716e-07, entry.InputCostPerToken, 1e-15)
	require.InDelta(t, 5.970149253731343e-07, *entry.Intervals[1].InputPrice, 1e-15)
}

func TestTkQianfanOverlay_ThinkSKUUsesIntervalOutputNotFlatThinkingRate(t *testing.T) {
	t.Parallel()
	entry := loadTKPricingOverlay()["deepseek-v3.2-think"]
	require.NotNil(t, entry)
	require.Zero(t, entry.ThinkingOutputCostPerToken,
		"dedicated think SKU with intervals must not pin a flat thinking_output rate")
	require.Len(t, entry.Intervals, 2)
	require.NotNil(t, entry.Intervals[1].OutputPrice)
	require.InDelta(t, 8.955223880597015e-07, *entry.Intervals[1].OutputPrice, 1e-15)
}

func TestTkQianfanOverlay_EmbeddingModelsUseEmbeddingMode(t *testing.T) {
	t.Parallel()
	for _, model := range []string{
		"bge-large-en",
		"bge-large-zh",
		"qwen3-embedding-0.6b",
		"qwen3-embedding-4b",
		"qwen3-embedding-8b",
	} {
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			entry := loadTKPricingOverlay()[model]
			require.NotNil(t, entry, model)
			require.Equal(t, "embedding", entry.Mode)
			require.Greater(t, entry.InputCostPerToken, 0.0)
			require.Zero(t, entry.OutputCostPerToken)
		})
	}
}
