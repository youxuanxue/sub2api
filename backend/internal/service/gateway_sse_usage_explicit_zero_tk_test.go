//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression for upstream #8407/#8408: final cumulative input counters may
// explicitly correct an earlier estimate to zero. Missing/invalid counters
// must not erase usage already observed from message_start.
func TestAnthropicSSEFinalInputCounters(t *testing.T) {
	parsers := map[string]func(string, *ClaudeUsage){
		"standard":    (&GatewayService{}).parseSSEUsage,
		"passthrough": parseSSEUsagePassthrough,
	}
	for name, parse := range parsers {
		t.Run(name, func(t *testing.T) {
			t.Run("cached input correction", func(t *testing.T) {
				usage := &ClaudeUsage{}
				parse(`{"type":"message_start","message":{"usage":{"input_tokens":8535,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`, usage)
				parse(`{"type":"message_delta","usage":{"input_tokens":0,"output_tokens":33,"cache_creation_input_tokens":0,"cache_read_input_tokens":4004}}`, usage)
				require.Equal(t, 0, usage.InputTokens, "cached input must not also be charged as uncached input")
				require.Equal(t, 4004, usage.CacheReadInputTokens)
				require.Equal(t, 33, usage.OutputTokens)
			})
			for _, field := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
				t.Run(field, func(t *testing.T) {
					for _, tc := range []struct {
						name, raw string
						want      int
					}{
						{"numeric zero", "0", 0},
						{"string zero", `"0"`, 0},
						{"positive", "42", 42},
						{"missing", "", 19},
						{"null", "null", 19},
						{"boolean", "false", 19},
						{"array", "[]", 19},
						{"object", "{}", 19},
						{"invalid string", `"invalid"`, 19},
						{"negative", "-1", 19},
						{"negative fraction", "-0.5", 19},
						{"positive fraction below one", "0.5", 19},
						{"positive fraction", "1.5", 19},
						{"overflow", "1e100", 19},
					} {
						t.Run(tc.name, func(t *testing.T) {
							usage := &ClaudeUsage{}
							parse(`{"type":"message_start","message":{"usage":{"input_tokens":19,"cache_creation_input_tokens":19,"cache_read_input_tokens":19}}}`, usage)
							fields := `"output_tokens":7`
							if tc.raw != "" {
								fields += fmt.Sprintf(",%q:%s", field, tc.raw)
							}
							parse(`{"type":"message_delta","usage":{`+fields+`}}`, usage)
							got := map[string]int{
								"input_tokens": usage.InputTokens, "cache_creation_input_tokens": usage.CacheCreationInputTokens,
								"cache_read_input_tokens": usage.CacheReadInputTokens,
							}
							for key, value := range got {
								want := 19
								if key == field {
									want = tc.want
								}
								require.Equal(t, want, value, key)
							}
							require.Equal(t, 7, usage.OutputTokens)
						})
					}
				})
			}
			t.Run("zero cache creation clears old and contradictory TTL charges", func(t *testing.T) {
				for _, details := range []string{"", `,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":12}`} {
					usage := &ClaudeUsage{}
					parse(`{"type":"message_start","message":{"usage":{"input_tokens":19,"cache_creation_input_tokens":19,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":12}}}}`, usage)
					parse(`{"type":"message_delta","usage":{"cache_creation_input_tokens":0`+details+`}}`, usage)
					require.Zero(t, usage.CacheCreationInputTokens)
					require.Zero(t, usage.CacheCreation5mTokens)
					require.Zero(t, usage.CacheCreation1hTokens)
					cost := (&BillingService{}).computeCacheCreationCost(&ModelPricing{
						SupportsCacheBreakdown: true, CacheCreation5mPrice: 2, CacheCreation1hPrice: 3,
					}, UsageTokens{
						CacheCreationTokens:   usage.CacheCreationInputTokens,
						CacheCreation5mTokens: usage.CacheCreation5mTokens, CacheCreation1hTokens: usage.CacheCreation1hTokens,
					}, 2, 1)
					require.Zero(t, cost, "cleared cache creation must not retain a charge")
				}
			})
		})
	}
}

func TestAnthropicSSEFinalCanonicalCountersOutrankAliases(t *testing.T) {
	t.Run("inclusive prompt normalization uses final cache buckets", func(t *testing.T) {
		usage := &ClaudeUsage{}
		parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"input_tokens":1200,"prompt_tokens":1200,"cache_read_input_tokens":0,"cached_tokens":800,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":100}}}`, usage)
		require.Equal(t, 1200, usage.InputTokens, "canonical zero cache must not subtract alias cache from total input")
		require.Zero(t, usage.CacheReadInputTokens)
		require.Zero(t, usage.CacheCreationInputTokens)
	})
	for _, raw := range []string{"0", "null", "-0.5", `"invalid"`} {
		t.Run(raw, func(t *testing.T) {
			usage := &ClaudeUsage{InputTokens: 19, CacheReadInputTokens: 13, CacheCreationInputTokens: 7}
			parseSSEUsagePassthrough(fmt.Sprintf(`{"type":"message_delta","usage":{"input_tokens":%s,"cache_read_input_tokens":%s,"cache_creation_input_tokens":%s,"cached_tokens":99,"prompt_tokens":150,"prompt_cache_hit_tokens":99,"prompt_cache_miss_tokens":51,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}`, raw, raw, raw), usage)
			if raw == "0" {
				require.Zero(t, usage.InputTokens)
				require.Zero(t, usage.CacheReadInputTokens)
				require.Zero(t, usage.CacheCreationInputTokens)
				require.Zero(t, usage.CacheCreation5mTokens)
				require.Zero(t, usage.CacheCreation1hTokens)
			} else {
				require.Equal(t, 19, usage.InputTokens)
				require.Equal(t, 13, usage.CacheReadInputTokens)
				require.Equal(t, 7, usage.CacheCreationInputTokens)
			}
		})
	}
	t.Run("missing canonical retains alias fallback", func(t *testing.T) {
		usage := &ClaudeUsage{}
		parseSSEUsagePassthrough(`{"type":"message_delta","usage":{"cached_tokens":99,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}}`, usage)
		require.Equal(t, 99, usage.CacheReadInputTokens)
		require.Equal(t, 5, usage.CacheCreationInputTokens)
	})
}
