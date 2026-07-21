//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

func atBJ(t *testing.T, h, m int) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	return time.Date(2026, 7, 21, h, m, 0, 0, loc)
}

func TestDeepSeekPeakMultiplierAt_Boundaries(t *testing.T) {
	require.InDelta(t, 2.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 9, 0)), 1e-9)
	require.InDelta(t, 2.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 11, 59)), 1e-9)
	require.InDelta(t, 1.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 12, 0)), 1e-9)
	require.InDelta(t, 1.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 13, 59)), 1e-9)
	require.InDelta(t, 2.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 14, 0)), 1e-9)
	require.InDelta(t, 2.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 17, 59)), 1e-9)
	require.InDelta(t, 1.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 18, 0)), 1e-9)
	require.InDelta(t, 1.0, tkDeepSeekPeakMultiplierAt(atBJ(t, 8, 59)), 1e-9)
}

func TestCalculateCostUnified_DeepSeekPeakDoublesOffPeakBase(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{"gpt-5.4":{"input_cost_per_token":0.0000025,"output_cost_per_token":0.000015,"litellm_provider":"openai","mode":"chat"}}`))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	resolver := NewModelPricingResolver(nil, billing)

	offPeak, err := billing.CalculateCostUnified(CostInput{
		Model:          "deepseek-v4-pro",
		Tokens:         UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		RateMultiplier: 1,
		Resolver:       resolver,
		BillingAt:      atBJ(t, 8, 0),
	})
	require.NoError(t, err)

	peak, err := billing.CalculateCostUnified(CostInput{
		Model:          "deepseek-v4-pro",
		Tokens:         UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		RateMultiplier: 1,
		Resolver:       resolver,
		BillingAt:      atBJ(t, 10, 0),
	})
	require.NoError(t, err)

	require.InDelta(t, offPeak.ActualCost*2, peak.ActualCost, 1e-9)
}

func TestCalculateCostUnified_DeepSeekPeakSkippedForChannelPricing(t *testing.T) {
	svc := &PricingService{}
	data, err := svc.parsePricingData([]byte(`{"gpt-5.4":{"input_cost_per_token":0.0000025,"output_cost_per_token":0.000015,"litellm_provider":"openai","mode":"chat"}}`))
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, &PricingService{pricingData: data})
	resolver := NewModelPricingResolver(nil, billing)
	resolved := &ResolvedPricing{
		Mode:   BillingModeToken,
		Source: PricingSourceChannel,
		BasePricing: &ModelPricing{
			InputPricePerToken:  0.000001,
			OutputPricePerToken: 0.000002,
		},
	}

	offPeak, err := billing.CalculateCostUnified(CostInput{
		Model:          "deepseek-v4-pro",
		Tokens:         UsageTokens{InputTokens: 1_000_000},
		RateMultiplier: 1,
		Resolver:       resolver,
		Resolved:       resolved,
		BillingAt:      atBJ(t, 8, 0),
	})
	require.NoError(t, err)

	peak, err := billing.CalculateCostUnified(CostInput{
		Model:          "deepseek-v4-pro",
		Tokens:         UsageTokens{InputTokens: 1_000_000},
		RateMultiplier: 1,
		Resolver:       resolver,
		Resolved:       resolved,
		BillingAt:      atBJ(t, 10, 0),
	})
	require.NoError(t, err)

	require.InDelta(t, offPeak.ActualCost, peak.ActualCost, 1e-12,
		"operator channel pricing must not receive automatic DeepSeek upstream peak scaling")
}

func TestAttachCatalogDeepSeekPeakValley(t *testing.T) {
	resp := &PublicCatalogResponse{
		Data: []PublicCatalogModel{{
			ModelID: "deepseek-v4-flash",
			Pricing: PublicCatalogPricing{
				Currency:          "USD",
				InputPer1KTokens:  0.14,
				OutputPer1KTokens: 0.28,
				CacheReadPer1K:    0.0028,
			},
		}},
	}
	attachCatalogDeepSeekPeakValley(resp)
	require.NotNil(t, resp.Data[0].Pricing.PeakValley)
	require.InDelta(t, 0.28, resp.Data[0].Pricing.PeakValley.InputPer1KTokens, 1e-9)
	require.Contains(t, resp.Data[0].Pricing.PeakValley.Windows, "09:00-12:00")
}
