//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TK: See upstream Wei-Shaw/sub2api#2539 — these tests pin the fallback that
// keeps group image pricing honored when the upstream pipeline leaves
// ForwardResult.ImageSize empty (e.g. the /v1/images/generations
// forwardOpenAIV1JSON path that sets ImageCount but never ImageSize, or any
// /responses image_generation path where the resolved size tier did not make
// it onto the result). Without this fallback, billing silently falls back to
// the hardcoded LiteLLM default price ($0.134) even though the group has
// explicitly configured image_price_1k / image_price_2k / image_price_4k.

// TestGetImageUnitPrice_EmptySize_UsesGroup2KPrice locks the core #2539 fix:
// when imageSize is empty and the group configured Price2K, billing applies
// the group's 2K override (matching normalizeOpenAIImageSizeTier's empty→2K
// behavior elsewhere in the pipeline).
func TestGetImageUnitPrice_EmptySize_UsesGroup2KPrice(t *testing.T) {
	svc := &BillingService{}

	price2K := 0.50
	groupConfig := &ImagePriceConfig{
		Price2K: &price2K,
	}

	cost := svc.CalculateImageCost("gpt-image-2", "", 1, groupConfig, 1.0)
	require.InDelta(t, 0.50, cost.TotalCost, 0.0001,
		"empty imageSize must apply group Price2K override, not fall back to default")
	require.InDelta(t, 0.50, cost.ActualCost, 0.0001)
}

// TestGetImageUnitPrice_EmptySize_FullGroupConfig_UsesGroup2KPrice covers the
// realistic case where the group configured every tier: empty size must still
// pick 2K (not 1K or 4K), matching the normalize-to-2K convention.
func TestGetImageUnitPrice_EmptySize_FullGroupConfig_UsesGroup2KPrice(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.30
	price2K := 0.50
	price4K := 1.00
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price2K: &price2K,
		Price4K: &price4K,
	}

	cost := svc.CalculateImageCost("gpt-image-2", "", 1, groupConfig, 1.0)
	require.InDelta(t, 0.50, cost.TotalCost, 0.0001,
		"empty imageSize must select the 2K tier from a fully-configured group")
}

// TestGetImageUnitPrice_EmptySize_GroupHasNo2K_FallsBackToDefault keeps the
// "no 2K override" case unchanged: empty size still falls through to the
// default LiteLLM price (basePrice without 1.5x multiplier) so groups that
// only configured 1K/4K are not surprised by a synthetic 2K bill.
func TestGetImageUnitPrice_EmptySize_GroupHasNo2K_FallsBackToDefault(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.30
	price4K := 1.00
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price4K: &price4K,
	}

	cost := svc.CalculateImageCost("gemini-3-pro-image", "", 1, groupConfig, 1.0)
	require.InDelta(t, 0.134, cost.TotalCost, 0.0001,
		"empty imageSize with no Price2K override must fall through to base default ($0.134)")
}

// TestGetImageUnitPrice_EmptySize_NoGroupConfig_PreservesDefault is the
// regression guard: when no group config exists at all, empty imageSize still
// returns the bare base price (not basePrice*1.5). This isolates the #2539 fix
// to the group-override path and avoids silently raising prices for
// ungrouped traffic.
func TestGetImageUnitPrice_EmptySize_NoGroupConfig_PreservesDefault(t *testing.T) {
	svc := &BillingService{}

	cost := svc.CalculateImageCost("gemini-3-pro-image", "", 1, nil, 1.0)
	require.InDelta(t, 0.134, cost.TotalCost, 0.0001,
		"empty imageSize without group config must preserve historical base-price behavior")
}

// TestGetImageUnitPrice_KnownSizes_StillRespectGroupOverrides is a regression
// guard so the empty-size fallback does not accidentally override the explicit
// 1K / 2K / 4K paths.
func TestGetImageUnitPrice_KnownSizes_StillRespectGroupOverrides(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.30
	price2K := 0.50
	price4K := 1.00
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price2K: &price2K,
		Price4K: &price4K,
	}

	cost := svc.CalculateImageCost("gpt-image-2", "1K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.30, cost.TotalCost, 0.0001)

	cost = svc.CalculateImageCost("gpt-image-2", "2K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.50, cost.TotalCost, 0.0001)

	cost = svc.CalculateImageCost("gpt-image-2", "4K", 1, groupConfig, 1.0)
	require.InDelta(t, 1.00, cost.TotalCost, 0.0001)
}

// TestCalculateImageCost_Issue2539_Repro reproduces the exact scenario from
// upstream Wei-Shaw/sub2api#2539: a group has image_price_1k/2k/4k
// configured, the request is image-billed (ImageCount > 0), but ImageSize
// arrives empty because the gateway path did not thread it through. Pre-fix
// behavior charged $0.134 (the default); post-fix charges $0.50 (group 2K).
func TestCalculateImageCost_Issue2539_Repro(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.30
	price2K := 0.50
	price4K := 1.00
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price2K: &price2K,
		Price4K: &price4K,
	}

	// 1 image, empty size — reproduces FourPrism's usage_logs row shape:
	//   billing_mode = image
	//   image_count  = 1
	//   image_size   = ''
	cost := svc.CalculateImageCost("gpt-image-2", "", 1, groupConfig, 1.0)

	require.InDelta(t, 0.50, cost.ActualCost, 0.0001,
		"#2539: empty image_size with group image pricing must NOT fall back to default $0.134")
	require.Equal(t, string(BillingModeImage), cost.BillingMode)
}
