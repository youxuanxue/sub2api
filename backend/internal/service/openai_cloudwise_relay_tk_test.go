//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeCloudwiseRelayBaseURL(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"https://api.cloudwise.ai/api", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/api/", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/v1", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/v1/", "https://api.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/api/v1", "https://api.cloudwise.ai/api", true},
		{"https://API.Cloudwise.AI/V1", "https://api.cloudwise.ai/api", true},
		{"https://api-us.cloudwise.ai", "https://api-us.cloudwise.ai/api", true},
		{"https://api-us.cloudwise.ai/v1", "https://api-us.cloudwise.ai/api", true},
		{"https://api.cloudwise.ai/other", "", false},
		{"https://api.openai.com", "", false},
		{"", "", false},
	} {
		got, ok := normalizeCloudwiseRelayBaseURL(tc.in)
		require.Equal(t, tc.wantOK, ok, tc.in)
		require.Equal(t, tc.want, got, tc.in)
	}

	// Host-only CloudWise URLs are the nginx HTML 404 path operators hit today.
	require.Equal(t, "https://api.cloudwise.ai/v1/responses", buildOpenAIResponsesURL("https://api.cloudwise.ai"))
	require.Equal(t, "https://api.cloudwise.ai/v1/chat/completions", buildOpenAIChatCompletionsURL("https://api.cloudwise.ai"))
	normalized, ok := normalizeCloudwiseRelayBaseURL("https://api.cloudwise.ai")
	require.True(t, ok)
	require.Equal(t, "https://api.cloudwise.ai/api/v1/responses", buildOpenAIResponsesURL(normalized))
	require.Equal(t, "https://api.cloudwise.ai/api/v1/chat/completions", buildOpenAIChatCompletionsURL(normalized))
}

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

func TestOpenAICloudwiseRelayUpstreamModelID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"MiniMax-M3", "MiniMax-M3"},
		{"minimax-m3", "MiniMax-M3"},
		{"MINIMAX-M3", "MiniMax-M3"},
		{"MiniMax-m3", "MiniMax-M3"},
		{"minimax-m2.7", "MiniMax-M2.7"},
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"deepseek-v4-pro", "deepseek-v4-pro"},
	} {
		require.Equal(t, tc.want, openAICloudwiseRelayUpstreamModelID(tc.in), tc.in)
	}
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
	for _, spell := range []string{"minimax-m3", "MINIMAX-M3", "MiniMax-m3"} {
		require.True(t, account.IsModelSupported(spell), spell)
		require.Equal(t, "MiniMax-M3", account.GetMappedModel(spell), spell)
	}
	for _, model := range []string{"gpt-5.4", "gemini-3-pro-preview", "qwen-plus"} {
		require.False(t, account.IsModelSupported(model), model)
	}
}
