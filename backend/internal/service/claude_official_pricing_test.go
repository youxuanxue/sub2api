//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Official Claude API list captured 2026-11-12 from
// https://platform.claude.com/docs/en/about-claude/pricing.
// Rates are USD per token. Fast mode, where the card lists it, is 2x input and output.
func TestClaudeOfficialListPrices(t *testing.T) {
	svc := newTestBillingService()
	cases := []struct {
		model                          string
		in, out, cache5m, cache1h, hit float64
		fast                           bool
	}{
		{"claude-fable-5-2", 8e-6, 40e-6, 10e-6, 16e-6, 0.2e-6, true},
		{"claude-fable-5-1", 10e-6, 50e-6, 12.5e-6, 20e-6, 0.25e-6, false},
		{"claude-fable-5", 10e-6, 50e-6, 12.5e-6, 20e-6, 1e-6, false},
		{"claude-mythos-5", 10e-6, 50e-6, 12.5e-6, 20e-6, 1e-6, false},
		{"claude-opus-5-5", 4e-6, 20e-6, 5e-6, 8e-6, 0.2e-6, true},
		{"claude-opus-5", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, true},
		{"claude-opus-4-8", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, true},
		{"claude-opus-4.8", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, true},
		{"claude-opus-4-7", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4.7", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4-6", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4.6", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4-5", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4.5", 5e-6, 25e-6, 6.25e-6, 10e-6, 0.5e-6, false},
		{"claude-opus-4-1", 15e-6, 75e-6, 18.75e-6, 30e-6, 1.5e-6, false},
		{"claude-opus-4-20250514", 15e-6, 75e-6, 18.75e-6, 30e-6, 1.5e-6, false},
		{"claude-sonnet-5-5", 2e-6, 10e-6, 2.5e-6, 4e-6, 0.1e-6, false},
		{"claude-sonnet-5", 2e-6, 10e-6, 2.5e-6, 4e-6, 0.2e-6, false},
		{"claude-sonnet-4-6", 3e-6, 15e-6, 3.75e-6, 6e-6, 0.3e-6, false},
		{"claude-sonnet-4-5", 3e-6, 15e-6, 3.75e-6, 6e-6, 0.3e-6, false},
		{"claude-sonnet-4", 3e-6, 15e-6, 3.75e-6, 6e-6, 0.3e-6, false},
		{"claude-haiku-4-5", 1e-6, 5e-6, 1.25e-6, 2e-6, 0.1e-6, false},
		{"claude-haiku-5-5", 0.1e-6, 0.5e-6, 0.125e-6, 0.2e-6, 0.01e-6, false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			p, err := svc.GetModelPricing(tc.model)
			require.NoError(t, err)
			require.InDelta(t, tc.in, p.InputPricePerToken, 1e-12)
			require.InDelta(t, tc.out, p.OutputPricePerToken, 1e-12)
			require.InDelta(t, tc.cache5m, p.CacheCreation5mPrice, 1e-12)
			require.InDelta(t, tc.cache1h, p.CacheCreation1hPrice, 1e-12)
			require.InDelta(t, tc.hit, p.CacheReadPricePerToken, 1e-12)
			require.True(t, p.SupportsCacheBreakdown)

			native := &Account{Platform: PlatformAnthropic}
			if tc.fast {
				require.NotNil(t, anthropicSpeedServiceTier(native, "fast", tc.model))
				standard, err := svc.CalculateCostWithServiceTier(tc.model, UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 1, "")
				require.NoError(t, err)
				fast, err := svc.CalculateCostWithServiceTier(tc.model, UsageTokens{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 1, "fast")
				require.NoError(t, err)
				require.InDelta(t, standard.InputCost*2, fast.InputCost, 1e-9)
				require.InDelta(t, standard.OutputCost*2, fast.OutputCost, 1e-9)
			} else {
				require.Nil(t, anthropicSpeedServiceTier(native, "fast", tc.model))
			}
		})
	}

	p, err := svc.GetModelPricing("claude-haiku-5-5")
	require.NoError(t, err)
	require.Equal(t, 100000, p.LongContextInputThreshold)
	require.InDelta(t, 5, p.LongContextInputMultiplier, 1e-12)
	require.InDelta(t, 5, p.LongContextOutputMultiplier, 1e-12)
	short, err := svc.CalculateCost("claude-haiku-5-5", UsageTokens{InputTokens: 100_000, OutputTokens: 1_000_000}, 1)
	require.NoError(t, err)
	long, err := svc.CalculateCost("claude-haiku-5-5", UsageTokens{InputTokens: 100_001, OutputTokens: 1_000_000}, 1)
	require.NoError(t, err)
	require.InDelta(t, 0.01, short.InputCost, 1e-9)
	require.InDelta(t, 0.5, short.OutputCost, 1e-9)
	require.InDelta(t, 100_001*0.1e-6*5, long.InputCost, 1e-9)
	require.InDelta(t, 2.5, long.OutputCost, 1e-9)
}
