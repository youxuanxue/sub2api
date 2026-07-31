//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func mustRegistryImageBasePrice(t *testing.T, model string) float64 {
	t.Helper()
	owner := tkOverlayLiteLLMModelPricing(model)
	require.NotNil(t, owner, "registry owner missing for %s", model)
	require.Positive(t, owner.OutputCostPerImage, "registry image price missing for %s", model)
	return owner.OutputCostPerImage
}

// TestCalculateImageCost_RegistryPricing tests the registry owner when no
// scoped group override is configured.
func TestCalculateImageCost_DefaultPricing(t *testing.T) {
	svc := &BillingService{}
	base := mustRegistryImageBasePrice(t, "gemini-3-pro-image")
	want2K := base * 1.5

	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, nil, 1.0)
	require.InDelta(t, want2K, cost.TotalCost, 1e-12)
	require.InDelta(t, want2K, cost.ActualCost, 1e-12)

	// 多张图片
	cost = svc.CalculateImageCost("gemini-3-pro-image", "2K", 3, nil, 1.0)
	require.InDelta(t, want2K*3, cost.TotalCost, 1e-12)
}

// TestCalculateImageCost_GroupCustomPricing 测试分组自定义价格
func TestCalculateImageCost_GroupCustomPricing(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.10
	price2K := 0.15
	price4K := 0.30
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price2K: &price2K,
		Price4K: &price4K,
	}

	// 1K 使用分组价格
	cost := svc.CalculateImageCost("gemini-3-pro-image", "1K", 2, groupConfig, 1.0)
	require.InDelta(t, 0.20, cost.TotalCost, 0.0001)

	// 2K 使用分组价格
	cost = svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.15, cost.TotalCost, 0.0001)

	// 4K 使用分组价格
	cost = svc.CalculateImageCost("gemini-3-pro-image", "4K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.30, cost.TotalCost, 0.0001)
}

func TestCalculateImageCost_NormalizesInvalidSizeTo2K(t *testing.T) {
	svc := &BillingService{}

	price2K := 0.25
	groupConfig := &ImagePriceConfig{Price2K: &price2K}

	for _, imageSize := range []string{"", "auto", "not-a-size"} {
		t.Run(imageSize, func(t *testing.T) {
			cost := svc.CalculateImageCost("gemini-3-pro-image", imageSize, 2, groupConfig, 1.0)
			require.InDelta(t, 0.50, cost.TotalCost, 0.0001)
			require.InDelta(t, 0.50, cost.ActualCost, 0.0001)
		})
	}
}

// TestCalculateImageCost_4KDoublePrice 测试 4K 默认价格翻倍
func TestCalculateImageCost_4KDoublePrice(t *testing.T) {
	svc := &BillingService{}
	base := mustRegistryImageBasePrice(t, "gemini-3-pro-image")

	cost := svc.CalculateImageCost("gemini-3-pro-image", "4K", 1, nil, 1.0)
	require.InDelta(t, base*2, cost.TotalCost, 1e-12)
}

// TestCalculateImageCost_RateMultiplier 测试费率倍数
func TestCalculateImageCost_RateMultiplier(t *testing.T) {
	svc := &BillingService{}
	base := mustRegistryImageBasePrice(t, "gemini-3-pro-image")
	want2K := base * 1.5

	// 费率倍数 1.5x
	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, nil, 1.5)
	require.InDelta(t, want2K, cost.TotalCost, 1e-12)
	require.InDelta(t, want2K*1.5, cost.ActualCost, 1e-12)

	// 费率倍数 2.0x
	cost = svc.CalculateImageCost("gemini-3-pro-image", "2K", 2, nil, 2.0)
	require.InDelta(t, want2K*2, cost.TotalCost, 1e-12)
	require.InDelta(t, want2K*2*2, cost.ActualCost, 1e-12)
}

// TestCalculateImageCost_ZeroCount 测试 imageCount=0
func TestCalculateImageCost_ZeroCount(t *testing.T) {
	svc := &BillingService{}

	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", 0, nil, 1.0)
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

// TestCalculateImageCost_NegativeCount 测试 imageCount=-1
func TestCalculateImageCost_NegativeCount(t *testing.T) {
	svc := &BillingService{}

	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", -1, nil, 1.0)
	require.Equal(t, 0.0, cost.TotalCost)
	require.Equal(t, 0.0, cost.ActualCost)
}

// TestCalculateImageCost_ZeroRateMultiplier 锁定新行为：倍率 0 直接按 0 计费
// （保存时已强制 > 0；若仍有 0 泄漏到计费层，零消耗比历史的 1.0 更安全）。
func TestCalculateImageCost_ZeroRateMultiplier(t *testing.T) {
	svc := &BillingService{}
	want2K := mustRegistryImageBasePrice(t, "gemini-3-pro-image") * 1.5

	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, nil, 0)
	require.InDelta(t, want2K, cost.TotalCost, 1e-12)
	require.InDelta(t, 0.0, cost.ActualCost, 1e-10)
}

// TestGetImageUnitPrice_GroupPriorityOverDefault 测试分组价格优先于默认价格
func TestGetImageUnitPrice_GroupPriorityOverDefault(t *testing.T) {
	svc := &BillingService{}

	price2K := 0.20
	groupConfig := &ImagePriceConfig{
		Price2K: &price2K,
	}

	// 分组配置了 2K 价格，应该覆盖 registry owner。
	cost := svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.20, cost.TotalCost, 0.0001)
}

// TestGetImageUnitPrice_PartialGroupConfig 测试分组部分配置时回退默认
func TestGetImageUnitPrice_PartialGroupConfig(t *testing.T) {
	svc := &BillingService{}
	base := mustRegistryImageBasePrice(t, "gemini-3-pro-image")

	// 只配置 1K 价格
	price1K := 0.10
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
	}

	// 1K 使用分组价格
	cost := svc.CalculateImageCost("gemini-3-pro-image", "1K", 1, groupConfig, 1.0)
	require.InDelta(t, 0.10, cost.TotalCost, 0.0001)

	// 未配置的 tier 继续使用 registry owner。
	cost = svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, groupConfig, 1.0)
	require.InDelta(t, base*1.5, cost.TotalCost, 1e-12)

	cost = svc.CalculateImageCost("gemini-3-pro-image", "4K", 1, groupConfig, 1.0)
	require.InDelta(t, base*2, cost.TotalCost, 1e-12)
}

// TestGetDefaultImagePrice_RegistryOwner verifies the embedded registry remains
// available even when the optional PricingService dependency is nil.
func TestGetDefaultImagePrice_RegistryOwner(t *testing.T) {
	svc := &BillingService{}
	base := mustRegistryImageBasePrice(t, "gemini-3-pro-image")

	cost := svc.CalculateImageCost("gemini-3-pro-image", "1K", 1, nil, 1.0)
	require.InDelta(t, base, cost.TotalCost, 1e-12)

	cost = svc.CalculateImageCost("gemini-3-pro-image", "2K", 1, nil, 1.0)
	require.InDelta(t, base*1.5, cost.TotalCost, 1e-12)
}

// TestGetImageUnitPrice_EmptyImageSize_UsesGroupTier locks the upstream
// Wei-Shaw/sub2api#2539 fix: when ForwardResult.ImageSize fails to propagate
// (image_size == ""), billing must still honor the group's configured image
// pricing — mirroring the request-side normalizeOpenAIImageSizeTier default
// (empty → "2K") — instead of silently bypassing the scoped override.
func TestGetImageUnitPrice_EmptyImageSize_UsesGroupTier(t *testing.T) {
	svc := &BillingService{}

	price1K := 0.30
	price2K := 0.50
	price4K := 1.00
	groupConfig := &ImagePriceConfig{
		Price1K: &price1K,
		Price2K: &price2K,
		Price4K: &price4K,
	}

	// Empty imageSize should be treated as the 2K tier and pick group's Price2K
	// ($0.50), not the registry owner price.
	cost := svc.CalculateImageCost("gpt-image-2", "", 1, groupConfig, 1.0)
	require.InDelta(t, 0.50, cost.TotalCost, 0.0001)
	require.InDelta(t, 0.50, cost.ActualCost, 0.0001)

	// Whitespace-only imageSize behaves the same as empty.
	cost = svc.CalculateImageCost("gpt-image-2", "   ", 2, groupConfig, 1.0)
	require.InDelta(t, 1.00, cost.TotalCost, 0.0001)
}

// TestGetImageUnitPrice_EmptyImageSize_NoGroupFallsBackTo2KDefault ensures the
// empty-imageSize normalization is consistent with the request-side default
// even when no group pricing is configured: use the registry owner at the 2K
// multiplier, not the prior 1K tier.
func TestGetImageUnitPrice_EmptyImageSize_NoGroupFallsBackTo2KDefault(t *testing.T) {
	svc := &BillingService{}
	want2K := mustRegistryImageBasePrice(t, "gemini-3-pro-image") * 1.5

	cost := svc.CalculateImageCost("gemini-3-pro-image", "", 1, nil, 1.0)
	require.InDelta(t, want2K, cost.TotalCost, 1e-12)
}

// TestGetImageUnitPrice_EmptyImageSize_PartialGroupConfigTier2K covers the
// realistic case from Wei-Shaw/sub2api#2539: customer configured only the 2K
// override and the request lost its size on the way to billing.
func TestGetImageUnitPrice_EmptyImageSize_PartialGroupConfigTier2K(t *testing.T) {
	svc := &BillingService{}

	price2K := 0.50
	groupConfig := &ImagePriceConfig{
		Price2K: &price2K,
	}

	cost := svc.CalculateImageCost("gpt-image-2", "", 1, groupConfig, 1.0)
	require.InDelta(t, 0.50, cost.TotalCost, 0.0001)
}
