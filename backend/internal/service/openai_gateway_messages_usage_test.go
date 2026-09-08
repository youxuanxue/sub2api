//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestCursorRelayedUsagePreservesBillingProvenance(t *testing.T) {
	for _, tier := range []string{"cursor-oauth-reported", "cursor-oauth-estimated"} {
		t.Run(tier, func(t *testing.T) {
			body := fmt.Sprintf(`{"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":3,"tk_billing_tier":%q}}`, tier)
			buffered := parseClaudeUsageFromResponseBody([]byte(body))
			require.Equal(t, tier, cursorBillingTier(buffered.BillingTier))
			var streamed ClaudeUsage
			parseSSEUsagePassthrough(`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`, &streamed)
			parseSSEUsagePassthrough(fmt.Sprintf(`{"type":"message_delta","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":3,"tk_billing_tier":%q}}`, tier), &streamed)
			require.Equal(t, *buffered, streamed)
			require.Equal(t, 13, claudeUsageToOpenAIUsage(&streamed).InputTokens)
		})
	}
	require.Empty(t, cursorBillingTier("untrusted-tier"))
}

func TestCopyOpenAIUsageFromResponsesUsageTrustsCanonicalCacheCreationValue(t *testing.T) {
	usage := &apicompat.ResponsesUsage{
		InputTokens:              20,
		OutputTokens:             2,
		CacheCreationInputTokens: 0,
		InputTokensDetails: &apicompat.ResponsesInputTokensDetails{
			CachedTokens:     3,
			CacheWriteTokens: 19,
		},
	}

	got := copyOpenAIUsageFromResponsesUsage(usage)

	require.Equal(t, 20, got.InputTokens)
	require.Equal(t, 3, got.CacheReadInputTokens)
	require.Zero(t, got.CacheCreationInputTokens)
}

func TestNativeAnthropicUsagePreservesDisjointBillingBuckets(t *testing.T) {
	got := claudeUsageToOpenAIUsage(&ClaudeUsage{InputTokens: 19338, OutputTokens: 91, CacheReadInputTokens: 13344, CacheCreationInputTokens: 20})
	require.Equal(t, 32702, got.InputTokens)
	require.Equal(t, 19338, got.InputTokens-got.CacheReadInputTokens-got.CacheCreationInputTokens)
	require.Equal(t, 91, got.OutputTokens)
	require.Equal(t, OpenAIUsage{}, claudeUsageToOpenAIUsage(nil))
}
