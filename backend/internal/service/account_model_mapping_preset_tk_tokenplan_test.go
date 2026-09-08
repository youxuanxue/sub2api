package service

import (
	"context"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestNewAPIModelMappingPresetIDsForAliTokenPlanAccount(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url": newapiintegration.AliTokenPlanBaseURL,
		},
	}
	want := newAPIAliTokenPlanModelMappingPresetIDs()
	require.NotEmpty(t, want, "manifest SSOT must expose Ali Token Plan preset ids")
	require.Contains(t, want, "qwen3.6-flash")
	require.NotContains(t, want, "deepseek-v4-flash-0731")
	require.NotContains(t, want, "deepseek-v4-pro")
	// PAYG-only ch17 floor ids must not leak into Token Plan override.
	for _, legacy := range []string{"qwen-plus", "qwen-max", "qwen-turbo"} {
		require.Contains(t, want, legacy, "legacy DashScope aliases must be routable on Token Plan")
	}

	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, want, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, len(want))
	for key, target := range mapping {
		require.False(t, strings.HasPrefix(key, "deepseek-") || strings.HasPrefix(key, "glm-"))
		require.False(t, strings.HasPrefix(target, "deepseek-") || strings.HasPrefix(target, "glm-"))
	}

	// PAYG DashScope on the same channel_type must keep the generic ch17 floor.
	payg := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url": "https://dashscope.aliyuncs.com",
		},
	}
	paygMapping, ok := accountModelMappingForAccount(context.Background(), payg, nil, nil, nil)
	require.True(t, ok)
	require.Contains(t, paygMapping, "qwen3-8b")
	require.Equal(t, "qwen3.7-plus", mapping["qwen-plus"])
	require.Equal(t, "qwen3.8-max", mapping["qwen-max"])
	require.Equal(t, "qwen3.8-flash", mapping["qwen-turbo"])
	for alias, target := range newAPIAliTokenPlanModelAliases() {
		require.Equal(t, target, mapping[alias])
		require.Equal(t, alias, paygMapping[alias], "hiding legacy names must not retarget or remove PAYG support")
		require.Contains(t, mapping, target, "alias target must be in the provider manifest")
		require.True(t, isTkCuratedNewAPIModelListed(alias), "legacy pricing membership remains")
		require.False(t, isTkCuratedNewAPIModelDisplayed(alias), "legacy names must not be advertised")
		require.NotContains(t, NewAPIModelDisplayIDsForAccount(account), alias)
		require.NotContains(t, NewAPIModelDisplayIDsForAccount(payg), alias)
	}
	require.NotEqual(t, want, NewAPIModelMappingPresetIDsForAccount(payg))
}

func TestNewAPIModelMappingPresetIDsForQianfanTokenPlanAccount(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanTokenPlanBaseURL,
		},
	}
	want := newAPIQianfanTokenPlanModelMappingPresetIDs()
	require.NotEmpty(t, want, "manifest SSOT must expose Qianfan Token Plan preset ids")
	require.Contains(t, want, "deepseek-v4-flash")
	require.Contains(t, want, "deepseek-v4-pro-0813")
	require.Contains(t, want, "glm-5.3-flash")
	require.NotContains(t, want, "ernie-5.0", "PAYG Qianfan ids must not leak into Token Plan override")

	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, want, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "deepseek-v4-pro-0813", mapping["deepseek-v4-pro-0813"])
	for alias, target := range newAPIQianfanTokenPlanModelAliases() {
		require.Equal(t, target, mapping[alias])
		require.Contains(t, mapping, target)
		require.NotContains(t, NewAPIModelDisplayIDsForAccount(account), alias)
	}
}

func TestAccountModelMappingFloorForOpsIncludesTokenPlanOverrides(t *testing.T) {
	t.Parallel()
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	require.NotEmpty(t, floor.AccountOverrides)

	var foundAli, foundQFPlan bool
	for _, override := range floor.AccountOverrides {
		if override.ChannelType == newapiconstant.ChannelTypeAli &&
			override.BaseURL == newapiintegration.AliTokenPlanBaseURL {
			foundAli = true
			require.Contains(t, override.ModelMapping, "qwen3.6-flash")
			require.NotContains(t, override.ModelMapping, "deepseek-v4-flash-0731")
			require.Contains(t, override.ModelMapping, "wan2.7-image")
			require.Contains(t, override.ModelMapping, "wan2.7-image-pro")
			require.Contains(t, override.ModelMapping, "qwen-audio-3.0-tts-plus")
			for alias, target := range newAPIAliTokenPlanModelAliases() {
				require.Equal(t, target, override.ModelMapping[alias])
			}
			require.NotContains(t, override.ModelMapping, "qwen-audio-3.0-realtime-plus")
		}
		if override.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
			override.BaseURL == newapiintegration.QianfanTokenPlanBaseURL {
			foundQFPlan = true
			require.Contains(t, override.ModelMapping, "kimi-k2.6")
			require.Contains(t, override.ModelMapping, "deepseek-v4-pro-0813")
			require.NotContains(t, override.ModelMapping, "ernie-5.0")
		}
	}
	require.True(t, foundAli, "Ali Token Plan account override must be exported in bundle floor")
	require.True(t, foundQFPlan, "Qianfan Token Plan account override must be exported in bundle floor")
}

func TestTokenPlanReconciliationRemovesAliSharedFamiliesOnly(t *testing.T) {
	t.Parallel()
	for _, baseURL := range []string{newapiintegration.AliTokenPlanBaseURL, "https://dashscope.aliyuncs.com"} {
		account := &Account{
			Platform: PlatformNewAPI, ChannelType: newapiconstant.ChannelTypeAli,
			Credentials: map[string]any{
				"base_url": baseURL,
				"model_mapping": map[string]string{
					"deepseek-v4-pro-0813": "deepseek-v4-pro",
					"glm-4.7":              "qwen3.7-plus",
					"custom-compatible":    "qwen3.7-plus",
				},
			},
		}
		required, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
		require.True(t, ok)
		got := reconciledAccountModelMapping(account, required)
		require.Equal(t, "qwen3.7-plus", got["custom-compatible"])
		if isNewAPIAliTokenPlanAccount(account) {
			require.NotContains(t, got, "deepseek-v4-pro-0813")
			require.NotContains(t, got, "glm-4.7", "GLM request names must not consume Ali even when targeting Qwen")
		} else {
			require.Contains(t, got, "deepseek-v4-pro-0813")
			require.Contains(t, got, "glm-4.7")
		}
		require.Equal(t, got, reconciledAccountModelMapping(account, got))
	}
}
