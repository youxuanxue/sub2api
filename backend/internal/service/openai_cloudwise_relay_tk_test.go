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

func TestCloudwiseRelayEmptyMappingStillRejectsForeignFamilies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		platform string
		baseURL  string
	}{
		{name: "openai_cn", platform: PlatformOpenAI, baseURL: "https://api.cloudwise.ai/api"},
		{name: "anthropic_us", platform: PlatformAnthropic, baseURL: "https://api-us.cloudwise.ai/api/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				Platform: tc.platform,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url": tc.baseURL,
				},
				Extra: map[string]any{"openai_passthrough": true},
			}
			for _, model := range []string{"claude-opus-4-8", "glm-5.3", "MiniMax-M3", "kimi-k3", "deepseek-v4-flash"} {
				require.True(t, account.IsModelSupported(model), model)
			}
			for _, model := range []string{"gpt-5.4", "gpt-5.4-mini", "gemini-3-pro-preview"} {
				require.False(t, account.IsModelSupported(model), model)
			}
		})
	}
}

func TestApplyOpenAICloudwiseRelayUpstreamModelID_AnthropicAccount(t *testing.T) {
	anthropic := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":      "https://api.cloudwise.ai/api",
			"model_mapping": modelMappingToAny(openAICloudwiseRelayWildcardModelMappingFloor()),
		},
	}
	require.True(t, isCloudwiseRelayAccount(anthropic))
	require.False(t, anthropic.IsOpenAICloudwiseRelay())
	require.Equal(t, "MiniMax-M3", applyOpenAICloudwiseRelayUpstreamModelID(anthropic, "minimax-m3"))
	require.Equal(t, "MiniMax-M3", anthropic.GetMappedModel("minimax-m3"))
}

func TestCloudwiseRelayPrefixGateOverridesExplicitGPTMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.cloudwise.ai/api",
			"model_mapping": map[string]any{
				"gpt-5.4":  "gpt-5.4",
				"claude-*": "claude-*",
			},
		},
	}
	require.False(t, account.IsModelSupported("gpt-5.4"))
	require.True(t, account.IsModelSupported("claude-sonnet-4-6"))
}

func TestGatewayService_CloudwiseRelayPassthroughEmptyMappingUsesPrefixGate(t *testing.T) {
	svc := &GatewayService{}
	for _, tc := range []struct {
		name     string
		platform string
		baseURL  string
	}{
		{name: "openai_cn", platform: PlatformOpenAI, baseURL: "https://api.cloudwise.ai/api"},
		{name: "anthropic_us", platform: PlatformAnthropic, baseURL: "https://api-us.cloudwise.ai/api/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				Platform: tc.platform,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"base_url": tc.baseURL,
				},
				Extra: map[string]any{"openai_passthrough": true},
			}
			for _, model := range []string{"claude-opus-4-8", "glm-5.3", "MiniMax-M3", "kimi-k3", "deepseek-v4-flash"} {
				require.True(t, svc.isModelSupportedByAccount(account, model), model)
			}
			for _, model := range []string{"gpt-5.4", "gpt-5.4-mini", "gemini-3-pro-preview"} {
				require.False(t, svc.isModelSupportedByAccount(account, model), model)
			}
		})
	}
}
