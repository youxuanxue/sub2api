//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Imagen bills its FLAT official per-image price: the 2K→×1.5 / 4K→×2 size-tier
// multiplier (upstream-owned, real only for genuine pixel-size tiers like
// Seedream) is exempted for imagen-* models. The assertion is about the
// MULTIPLIER, so the exact registry price is irrelevant: Imagen must return its
// owner price unchanged for every tier.
func TestImagenBillsFlat_NoSizeTierMultiplier(t *testing.T) {
	svc := &BillingService{} // registry snapshot is used directly

	ultraBase := mustRegistryImageBasePrice(t, "imagen-4.0-ultra-generate-001")
	for _, size := range []string{"1K", "2K", "4K", "", "auto"} {
		cost := svc.CalculateImageCost("imagen-4.0-ultra-generate-001", size, 1, nil, 1.0)
		require.InDeltaf(t, ultraBase, cost.TotalCost, 1e-12,
			"imagen must bill flat base regardless of size tier (size=%q)", size)
	}

	// Scales linearly by n, still flat per image (not ×1.5 per image).
	fastBase := mustRegistryImageBasePrice(t, "imagen-4.0-fast-generate-001")
	cost := svc.CalculateImageCost("imagen-4.0-fast-generate-001", "2K", 3, nil, 1.0)
	require.InDelta(t, fastBase*3, cost.TotalCost, 1e-12)
}

// The exemption is scoped to Imagen ONLY — models with genuine size tiers keep
// the multiplier. This is the guard against a global regression.
func TestImageSizeMultiplier_StillAppliesToNonImagen(t *testing.T) {
	svc := &BillingService{}

	geminiBase := mustRegistryImageBasePrice(t, "gemini-3-pro-image")
	require.InDelta(t, geminiBase*1.5, svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, nil, 1.0).TotalCost, 1e-12)
	require.InDelta(t, geminiBase*2, svc.CalculateImageCost("gemini-3-pro-image", "4K", 1, nil, 1.0).TotalCost, 1e-12)
	// Seedream sends real pixel sizes — 2K still ×1.5.
	seedreamBase := mustRegistryImageBasePrice(t, "seedream-4-0-250828")
	require.InDelta(t, seedreamBase*1.5, svc.CalculateImageCost("seedream-4-0-250828", "2K", 1, nil, 1.0).TotalCost, 1e-12)
}

// The pre-flight HOLD shares getDefaultImagePrice (EstimateImageHold forces an
// empty size to "4K"), so the same exemption collapses Imagen's hold to flat base
// — removing the marginal over-reserve / wrong-403 for thin-balance keys. Seedream
// keeps the 4K tier-max hold.
func TestImagenHold_FlatNotFourKMax(t *testing.T) {
	svc := &BillingService{}

	fastBase := mustRegistryImageBasePrice(t, "imagen-4.0-fast-generate-001")
	seedreamBase := mustRegistryImageBasePrice(t, "seedream-4-0-250828")
	require.InDelta(t, fastBase, svc.EstimateImageHold("imagen-4.0-fast-generate-001", "", 1, nil, 1.0), 1e-12)
	require.InDelta(t, seedreamBase*2, svc.EstimateImageHold("seedream-4-0-250828", "", 1, nil, 1.0), 1e-12)
}
