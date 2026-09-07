//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

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
