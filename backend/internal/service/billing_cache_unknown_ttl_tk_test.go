//go:build unit

package service

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCacheCreationUnknownTTLUsesOneHourPrice(t *testing.T) {
	svc := &BillingService{}
	pricing := &ModelPricing{SupportsCacheBreakdown: true, CacheCreation5mPrice: 2, CacheCreation1hPrice: 5}
	for _, tc := range []struct {
		name   string
		tokens UsageTokens
		want   float64
	}{
		{"partial five minute", UsageTokens{CacheCreationTokens: 11000, CacheCreation5mTokens: 3000}, 46000},
		{"partial one hour", UsageTokens{CacheCreationTokens: 11000, CacheCreation1hTokens: 3000}, 55000},
		{"partial mixed", UsageTokens{CacheCreationTokens: 11000, CacheCreation5mTokens: 2000, CacheCreation1hTokens: 1000}, 49000},
		{"aggregate only", UsageTokens{CacheCreationTokens: 8000}, 40000},
		{"complete details", UsageTokens{CacheCreationTokens: 11000, CacheCreation5mTokens: 3000, CacheCreation1hTokens: 8000}, 46000},
		{"contradictory details remain capped", UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 90, CacheCreation1hTokens: 60}, 320},
		{"zero usage", UsageTokens{}, 0},
		{"max integer aggregate", UsageTokens{CacheCreationTokens: math.MaxInt, CacheCreation5mTokens: 1}, 2 + float64(math.MaxInt-1)*5},
		{"negative aggregate keeps reported details", UsageTokens{CacheCreationTokens: -1, CacheCreation5mTokens: 30, CacheCreation1hTokens: 60}, 360},
		{"absent aggregate keeps reported details", UsageTokens{CacheCreation5mTokens: 30, CacheCreation1hTokens: 60}, 360},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.tokens
			require.Equal(t, tc.want, svc.computeCacheCreationCost(pricing, tc.tokens, 9, 1))
			require.Equal(t, tc.want*2, svc.computeCacheCreationCost(pricing, tc.tokens, 9, 2), "long context multiplier includes unknown TTL")
			require.Equal(t, before, tc.tokens, "settlement must not invent raw TTL usage")
		})
	}
}

func TestCacheCreationUnknownTTLUsesResolvedPricing(t *testing.T) {
	svc := &BillingService{}
	tokens := UsageTokens{CacheCreationTokens: 100, CacheCreation5mTokens: 20}
	flat := 3.0
	pricing := &ModelPricing{SupportsCacheBreakdown: true, CacheCreation5mPrice: 2, CacheCreation1hPrice: 5}
	applyChannelTokenPriceOverrides(pricing, &ChannelModelPricing{CacheWritePrice: &flat})
	require.Equal(t, 300.0, svc.computeCacheCreationCost(pricing, tokens, flat, 1), "existing flat override owns both TTL prices")
	zero := 0.0
	applyChannelTokenPriceOverrides(pricing, &ChannelModelPricing{CacheWrite1hPrice: &zero})
	require.Equal(t, 60.0, svc.computeCacheCreationCost(pricing, tokens, flat, 1), "an explicitly free 1h price must remain free")
	applyChannelTokenPriceOverrides(pricing, &ChannelModelPricing{CacheWritePrice: &zero, CacheWrite1hPrice: &zero})
	require.Zero(t, svc.computeCacheCreationCost(pricing, tokens, pricing.CacheCreationPricePerToken, 1), "explicitly free channel pricing keeps all cache creation free")
	require.Equal(t, 700.0, svc.computeCacheCreationCost(&ModelPricing{}, tokens, 7, 1), "non-TTL models keep the standard cache write price")
}

func TestAnthropicSSECumulativeCacheCreationUnknownTTLSettlement(t *testing.T) {
	pricing := &ModelPricing{SupportsCacheBreakdown: true, CacheCreation5mPrice: 2, CacheCreation1hPrice: 5}
	for parser, parse := range map[string]func(string, *ClaudeUsage){"standard": (&GatewayService{}).parseSSEUsage, "passthrough": parseSSEUsagePassthrough} {
		for _, tc := range []struct {
			name             string
			five, one, total int
			want             float64
		}{
			{"partial five minute", 3000, 0, 11000, 46000},
			{"partial one hour", 0, 3000, 11000, 55000},
			{"aggregate only", 0, 0, 8000, 40000},
			{"explicit zero clears old details", 3000, 0, 0, 0},
		} {
			t.Run(parser+"/"+tc.name, func(t *testing.T) {
				usage := &ClaudeUsage{}
				parse(fmt.Sprintf(`{"type":"message_start","message":{"usage":{"cache_creation_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}}}}`, tc.five+tc.one, tc.five, tc.one), usage)
				parse(fmt.Sprintf(`{"type":"message_delta","usage":{"cache_creation_input_tokens":%d}}`, tc.total), usage)
				before := *usage
				tokens := UsageTokens{CacheCreationTokens: usage.CacheCreationInputTokens, CacheCreation5mTokens: usage.CacheCreation5mTokens, CacheCreation1hTokens: usage.CacheCreation1hTokens}
				require.Equal(t, tc.want, (&BillingService{}).computeCacheCreationCost(pricing, tokens, 0, 1))
				require.Equal(t, before, *usage)
				require.Equal(t, tc.total, usage.CacheCreationInputTokens)
				if tc.total == 0 {
					require.Zero(t, usage.CacheCreation5mTokens)
					require.Zero(t, usage.CacheCreation1hTokens)
				} else {
					require.Equal(t, tc.five, usage.CacheCreation5mTokens)
					require.Equal(t, tc.one, usage.CacheCreation1hTokens)
				}
			})
		}
	}
}
