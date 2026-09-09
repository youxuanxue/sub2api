//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProtocolUsageCacheBucketsRoundTrip(t *testing.T) {
	native := &ForwardResult{Usage: ClaudeUsage{InputTokens: 7, OutputTokens: 5, CacheReadInputTokens: 3, CacheCreationInputTokens: 2, ImageOutputTokens: 1}}
	openai := OpenAIForwardResultFromForward(native)
	require.Equal(t, 12, openai.Usage.InputTokens)
	require.Equal(t, 3, openai.Usage.CacheReadInputTokens)
	require.Equal(t, 2, openai.Usage.CacheCreationInputTokens)
	require.Equal(t, native.Usage, ForwardResultFromOpenAI(openai).Usage)
	malformed := ForwardResultFromOpenAI(&OpenAIForwardResult{Usage: OpenAIUsage{InputTokens: 1, CacheReadInputTokens: 2}})
	require.Zero(t, malformed.Usage.InputTokens)
}
