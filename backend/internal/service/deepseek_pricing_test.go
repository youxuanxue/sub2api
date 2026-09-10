//go:build unit

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TokenKey registry policy: official peak windows effective 2026-08-23.
// 高峰时段 01:00–04:00 与 06:00–10:00 UTC（半开区间，仅工作日）；
// 北京时间周六/周日全天低谷；高峰价 = 2× 低谷价。
// 2026-08-24 为周一（工作日），2026-08-22 周六、2026-08-23 周日。
// ---------------------------------------------------------------------------

func TestDeepseekPeakMultiplierAt(t *testing.T) {
	mon := func(hour, min int) time.Time { return time.Date(2026, 8, 24, hour, min, 0, 0, time.UTC) }
	sat := func(hour, min int) time.Time { return time.Date(2026, 8, 22, hour, min, 0, 0, time.UTC) }
	sun := func(hour, min int) time.Time { return time.Date(2026, 8, 23, hour, min, 0, 0, time.UTC) }

	tests := []struct {
		name string
		now  time.Time
		want float64
	}{
		// 工作日高峰窗口边界（半开区间）
		{"weekday 01:00 peak start", mon(1, 0), 2.0},
		{"weekday 03:59 peak upper bound", mon(3, 59), 2.0},
		{"weekday 04:00 peak end", mon(4, 0), 1.0},
		{"weekday 06:00 peak start", mon(6, 0), 2.0},
		{"weekday 09:59 peak upper bound", mon(9, 59), 2.0},
		{"weekday 10:00 peak end", mon(10, 0), 1.0},
		// 工作日低谷时段
		{"weekday 00:00 off-peak", mon(0, 0), 1.0},
		{"weekday 05:00 off-peak", mon(5, 0), 1.0},
		{"weekday 12:00 off-peak", mon(12, 0), 1.0},
		{"weekday 23:59 off-peak", mon(23, 59), 1.0},
		// 北京时间周末全天低谷（即使 UTC 处于高峰时段）
		{"saturday utc 02:00 beijing sat 10:00", sat(2, 0), 1.0},
		{"sunday utc 07:00 beijing sun 15:00", sun(7, 0), 1.0},
		// 北京时间与 UTC 跨日边界：UTC 周六 16:30 = 北京周日 00:30 → 周末低谷
		{"utc saturday 16:30 = beijing sunday 00:30", sat(16, 30), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tkDeepSeekPeakMultiplierAtWithPolicy(loadTkDeepSeekPeakValleyPolicy(), tt.now))
		})
	}
}

func TestIsDeepSeekModel(t *testing.T) {
	deepseek := []string{
		"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp",
		"deepseek-chat", "deepseek-reasoner", "deepseek-v3-2-251201",
		"deepseek-coder", "deepseek-foo", "deepseek-v4-pro-0813",
		"DEEPSEEK-V4-PRO", " deepseek-v4-flash ",
	}
	for _, m := range deepseek {
		require.True(t, isDeepSeekModel(m), "model %q should be deepseek", m)
	}

	nonDeepseek := []string{
		"gpt-5.4", "claude-sonnet-4", "deepseekcoder", // 无连字符不算 deepseek- 前缀
		"", " deepseek", // 无连字符后缀
	}
	for _, m := range nonDeepseek {
		require.False(t, isDeepSeekModel(m), "model %q should not be deepseek", m)
	}
}

// ---------------------------------------------------------------------------
// 默认价卡（Source=LiteLLM）按官方峰谷倍率计费；分组/渠道自定义定价不叠加
// ---------------------------------------------------------------------------

func TestCalculateCostUnified_DeepseekDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := tkCNYPerMTokToUSDPerToken(1000*1.5+500*4.5+1000*0.05) * tkOfficialListBaseTaxMultiplier()

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 周一低谷
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekProDefaultCardPeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := tkCNYPerMTokToUSDPerToken(1000*4.5+500*13.5+1000*0.15) * tkOfficialListBaseTaxMultiplier()

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-pro", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 6, 30, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekVersionedNamePeakMultiplier(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	offPeakTotal := tkCNYPerMTokToUSDPerToken(1000*1.5+500*4.5+1000*0.05) * tkOfficialListBaseTaxMultiplier()

	offPeak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 周一低谷
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal, offPeak.TotalCost, 1e-10)

	peak, err := bs.CalculateCostUnified(CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash-0731", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
		PricingAt: time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC), // 周一高峰
	})
	require.NoError(t, err)
	require.InDelta(t, offPeakTotal*2, peak.TotalCost, 1e-10)
}

func TestCalculateCostUnified_DeepseekGroupPricingNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	inputPrice := 1e-6
	outputPrice := 2e-6
	group := &Group{
		ID: 1, Name: "ds-group", Platform: PlatformDeepseek, Status: StatusActive,
		ModelPricing: []ChannelModelPricing{{
			Models: []string{"deepseek-v4-flash"}, BillingMode: BillingModeToken,
			InputPrice: &inputPrice, OutputPrice: &outputPrice,
		}},
	}
	resolved := resolver.Resolve(context.Background(), PricingInput{Model: "deepseek-v4-flash", Group: group})
	require.Equal(t, PricingSourceGroup, resolved.Source)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 1000}
	// Custom input/output prices retain the registry's taxed cache-read price.
	groupTotal := 1000*1e-6 + 500*2e-6 + 1000*tkCNYPerMTokToUSDPerToken(0.05)*tkOfficialListBaseTaxMultiplier()

	for _, pricingAt := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC), // 低谷
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),  // 高峰
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "deepseek-v4-flash", Group: group,
			Tokens: tokens, RateMultiplier: 1.0, Resolver: resolver, PricingAt: pricingAt,
		})
		require.NoError(t, err)
		require.InDelta(t, groupTotal, cost.TotalCost, 1e-10,
			"分组自定义定价不应叠加官方峰谷倍率（pricingAt=%v）", pricingAt)
	}
}

func TestCalculateCostUnified_NonDeepseekDefaultCardNotScaledByPeak(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	total := 1000*3e-6 + 500*15e-6 // claude-sonnet-4 fallback

	for _, pricingAt := range []time.Time{
		time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 24, 2, 0, 0, 0, time.UTC),
	} {
		cost, err := bs.CalculateCostUnified(CostInput{
			Ctx: context.Background(), Model: "claude-sonnet-4", Tokens: tokens,
			RateMultiplier: 1.0, Resolver: resolver, PricingAt: pricingAt,
		})
		require.NoError(t, err)
		require.InDelta(t, total, cost.TotalCost, 1e-10,
			"非 DeepSeek 模型不应受官方峰谷倍率影响（pricingAt=%v）", pricingAt)
	}
}

func TestCalculateCostUnified_DeepseekPricingAtZeroFallsBackToNow(t *testing.T) {
	bs := newTestBillingService()
	resolver := NewModelPricingResolver(nil, bs)

	tokens := UsageTokens{InputTokens: 1000, OutputTokens: 500}
	base := CostInput{
		Ctx: context.Background(), Model: "deepseek-v4-flash", Tokens: tokens,
		RateMultiplier: 1.0, Resolver: resolver,
	}

	// PricingAt 零值 → 回退 timezone.Now()，与显式传入当前时刻结果一致。
	costZero, err := bs.CalculateCostUnified(base)
	require.NoError(t, err)

	costNow, err := bs.CalculateCostUnified(CostInput{
		Ctx: base.Ctx, Model: base.Model, Tokens: base.Tokens,
		RateMultiplier: base.RateMultiplier, Resolver: base.Resolver,
		PricingAt: timezone.Now(),
	})
	require.NoError(t, err)
	require.Equal(t, costZero.TotalCost, costNow.TotalCost)
}

// ---------------------------------------------------------------------------
// Registry prices override external documents; unknown models fail closed.
// ---------------------------------------------------------------------------

func TestGetModelPricing_DeepseekForcesOfficialRatesOverJSON(t *testing.T) {
	pricingSvc := NewPricingService(&config.Config{}, nil)
	data, err := pricingSvc.parsePricingData([]byte(`{
		"deepseek-v4-flash": {"input_cost_per_token": 1, "output_cost_per_token": 2},
		"deepseek-v4-pro": {"input_cost_per_token": 1, "output_cost_per_token": 2},
		"deepseek-chat": {"input_cost_per_token": 1, "output_cost_per_token": 2}
	}`))
	require.NoError(t, err)
	pricingSvc.pricingData = data
	require.InDelta(t, tkCNYPerMTokToUSDPerToken(1.5), data["deepseek-v4-flash"].InputCostPerToken, 1e-15)
	bs := NewBillingService(&config.Config{}, pricingSvc)

	tests := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		{"deepseek-v4-flash", 1.5, 4.5, 0.05},
		{"deepseek-v4-flash-vision-exp", 1.5, 4.5, 0.05},
		{"deepseek-v4-pro", 4.5, 13.5, 0.15},
		{"deepseek-chat", 1.5, 4.5, 0.05},
		{"deepseek-reasoner", 1.5, 4.5, 0.05},
		{"deepseek-v4-pro-0813", 4.5, 13.5, 0.15},
		{"deepseek-v4-flash-0731", 1.5, 4.5, 0.05},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(tt.model)
			require.NoError(t, err)
			tax := tkOfficialListBaseTaxMultiplier()
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.input)*tax, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.output)*tax, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.cacheRead)*tax, pricing.CacheReadPricePerToken, 1e-15)
		})
	}
}

func TestGetModelPricing_UnknownDeepseekFailsClosed(t *testing.T) {
	pricingSvc := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek-unknown": {InputCostPerToken: 0, OutputCostPerToken: 0},
	}}
	bs := NewBillingService(&config.Config{}, pricingSvc)

	for _, m := range []string{"deepseek-unknown", "deepseek-foo"} {
		t.Run(m, func(t *testing.T) {
			pricing, err := bs.GetModelPricing(m)
			require.ErrorIs(t, err, ErrModelPricingUnavailable)
			require.Nil(t, pricing)
		})
	}
}

// ---------------------------------------------------------------------------
// 本地兜底 JSON：无 $0 占位条目，官方模型价格为官方低谷价
// ---------------------------------------------------------------------------

func TestDeepseekPricingFileMatchesOfficialRates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)

	pricingSvc := &PricingService{}
	pricingData, err := pricingSvc.parsePricingData(data)
	require.NoError(t, err)

	tests := []struct {
		model                    string
		input, output, cacheRead float64
	}{
		{"deepseek-v4-flash", 1.5, 4.5, 0.05},
		{"deepseek-v4-pro", 4.5, 13.5, 0.15},
		{"deepseek-chat", 1.5, 4.5, 0.05},
		{"deepseek-reasoner", 1.5, 4.5, 0.05},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			entry, ok := pricingData[tt.model]
			require.True(t, ok, "model %s must exist in pricing file", tt.model)
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.input), entry.InputCostPerToken, 1e-15)
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.output), entry.OutputCostPerToken, 1e-15)
			require.InDelta(t, tkCNYPerMTokToUSDPerToken(tt.cacheRead), entry.CacheReadInputTokenCost, 1e-15)
		})
	}
}
