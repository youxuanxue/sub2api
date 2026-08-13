//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICloudwiseRelayAllowedModelPrefixes(t *testing.T) {
	require.Equal(t, []string{
		"kimi-",
		"claude-",
		"glm-",
		"minimax-",
		"deepseek-",
	}, openAICloudwiseRelayAllowedModelPrefixes)
}

func TestOpenAICloudwiseRelayWildcardModelMappingFloor(t *testing.T) {
	mapping := openAICloudwiseRelayWildcardModelMappingFloor()
	require.Equal(t, map[string]string{
		"kimi-*":     "kimi-*",
		"claude-*":   "claude-*",
		"glm-*":      "glm-*",
		"minimax-*":  "minimax-*",
		"deepseek-*": "deepseek-*",
	}, mapping)
}

func TestCloudwiseRelayAccountSupportsPrefixFamiliesOnly(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":      "https://api.cloudwise.ai/api",
			"model_mapping": modelMappingToAny(openAICloudwiseRelayWildcardModelMappingFloor()),
		},
	}
	require.True(t, account.IsOpenAICloudwiseRelay())

	for _, model := range []string{"claude-opus-4-6", "kimi-k3", "glm-5", "MiniMax-M3", "deepseek-v4-flash"} {
		require.True(t, account.IsModelSupported(model), model)
		require.Equal(t, model, account.GetMappedModel(model), model)
	}
	for _, model := range []string{"gpt-5.4", "gemini-3-pro-preview", "qwen-plus"} {
		require.False(t, account.IsModelSupported(model), model)
	}
}
