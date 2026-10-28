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
	for _, model := range []string{
		"glm-5.2",
		"glm-5.3",
		"happyhorse-1.1-t2v",
		"happyhorse-1.1-i2v",
		"happyhorse-1.1-r2v",
		"qwen-image-3.0-pro",
		"wan3.0-video",
	} {
		require.Contains(t, want, model)
	}
	for _, deepseek := range []string{
		"deepseek-chat",
		"deepseek-flash",
		"deepseek-v4-flash",
		"deepseek-v4-flash-0731",
		"deepseek-v4-pro",
		"deepseek-v4-pro-0813",
		"deepseek-v4.1-flash",
	} {
		require.NotContains(t, want, deepseek)
	}
	for _, stale := range []string{"glm-4.5", "glm-4.5-air", "glm-4.6", "glm-4.7", "glm-5", "glm-5.1"} {
		require.NotContains(t, want, stale)
	}
	// PAYG-only ch17 floor ids must not leak into Token Plan override.
	for _, legacy := range []string{"qwen-plus", "qwen-max"} {
		require.Contains(t, want, legacy, "legacy DashScope aliases must be routable on Token Plan")
	}

	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, want, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, len(want))
	require.NotContains(t, mapping, "deepseek-flash")
	require.NotContains(t, mapping, "deepseek-v4-flash")
	require.NotContains(t, mapping, "deepseek-v4-flash-0731")
	require.NotContains(t, mapping, "deepseek-v4-pro-0813")
	require.NotContains(t, mapping, "deepseek-v4.1-flash")
	require.Equal(t, "glm-5.2", mapping["glm-5.2"])
	require.Equal(t, "glm-5.3", mapping["glm-5.3"])

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
	for _, retired := range aliTokenPlanForbiddenModelMappingKeys {
		require.NotContains(t, mapping, retired)
		require.NotContains(t, got, retired)
	}
	// Withdrawn Plan aliases retain their PAYG identities.
	for _, retired := range aliTokenPlanForbiddenModelMappingKeys {
		if _, exists := paygMapping[retired]; exists {
			require.Equal(t, retired, paygMapping[retired])
		}
	}
	for alias, target := range newAPIAliTokenPlanModelAliases() {
		require.Equal(t, target, mapping[alias])
		if strings.HasPrefix(alias, "qwen") {
			require.Equal(t, alias, paygMapping[alias], "hiding legacy names must not retarget or remove PAYG support")
		} else {
			require.NotContains(t, paygMapping, alias, "Ali Token Plan aliases must not leak into PAYG")
		}
		require.Contains(t, mapping, target, "alias target must be in the provider manifest")
		if strings.HasPrefix(alias, "qwen") {
			require.True(t, isTkCuratedNewAPIModelListed(alias), "legacy pricing membership remains")
			require.False(t, isTkCuratedNewAPIModelDisplayed(alias), "legacy names must not be advertised")
			require.NotContains(t, NewAPIModelDisplayIDsForAccount(account), alias)
			require.NotContains(t, NewAPIModelDisplayIDsForAccount(payg), alias)
		}
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
	require.Contains(t, want, "deepseek-v4.1-flash")
	require.Contains(t, want, "deepseek-flash")
	require.Contains(t, want, "glm-5.3")
	require.Contains(t, want, "deepseek-v4-pro-0813")
	require.Contains(t, want, "glm-5.3-flash")
	require.NotContains(t, want, "deepseek-v4-flash")
	require.NotContains(t, want, "kimi-k2.6")
	require.NotContains(t, want, "glm-4.5-air")
	require.NotContains(t, want, "glm-4.7")
	require.NotContains(t, want, "ernie-5.0", "PAYG Qianfan ids must not leak into Token Plan override")

	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, want, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "deepseek-v4-pro-0813", mapping["deepseek-v4-pro-0813"])
	require.Equal(t, "deepseek-v4.1-flash", mapping["deepseek-v4.1-flash"])
	require.Equal(t, "deepseek-v4.1-flash", mapping["deepseek-flash"])
	require.Equal(t, "glm-5.3", mapping["glm-5.3"])
	for _, withdrawn := range qianfanTokenPlanForbiddenModelMappingKeys {
		require.NotContains(t, mapping, withdrawn)
	}
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
			for _, model := range []string{
				"glm-5.2",
				"glm-5.3",
			} {
				require.Contains(t, override.ModelMapping, model)
			}
			for _, deepseek := range []string{
				"deepseek-chat",
				"deepseek-flash",
				"deepseek-v4-flash",
				"deepseek-v4-flash-0731",
				"deepseek-v4-pro",
				"deepseek-v4-pro-0813",
				"deepseek-v4.1-flash",
			} {
				require.NotContains(t, override.ModelMapping, deepseek)
			}
			require.Contains(t, override.ModelMapping, "wan2.7-image")
			require.Contains(t, override.ModelMapping, "wan2.7-image-pro")
			require.Contains(t, override.ModelMapping, "qwen-audio-3.0-tts-plus")
			require.Equal(t, "qwen-image-3.0-pro", override.ModelMapping["qwen-image-3.0-pro"])
			require.Equal(t, "wan3.0-video", override.ModelMapping["wan3.0-video"])
			require.NotContains(t, override.ModelMapping, "deepseek-v4-flash")
			for _, video := range []string{
				"happyhorse-1.1-t2v",
				"happyhorse-1.1-i2v",
				"happyhorse-1.1-r2v",
			} {
				require.Contains(t, override.ModelMapping, video)
			}
			for _, stale := range []string{
				"glm-4.5", "glm-4.5-air", "glm-4.6", "glm-4.7", "glm-5", "glm-5.1",
				"text-embedding-v1", "text-embedding-v4",
			} {
				require.NotContains(t, override.ModelMapping, stale)
			}
			scope := "account_override:" + normalizeAccountModelMappingOverrideScope(
				PlatformNewAPI, newapiconstant.ChannelTypeAli, newapiintegration.AliTokenPlanBaseURL,
			)
			require.ElementsMatch(t, aliTokenPlanForbiddenModelMappingKeys, floor.ForbiddenModelMappingKeys[scope])
			for alias, target := range newAPIAliTokenPlanModelAliases() {
				require.Equal(t, target, override.ModelMapping[alias])
			}
			require.NotContains(t, override.ModelMapping, "qwen-audio-3.0-realtime-plus")
		}
		if override.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
			override.BaseURL == newapiintegration.QianfanTokenPlanBaseURL {
			foundQFPlan = true
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-v4.1-flash"])
			require.Equal(t, "deepseek-v4.1-flash", override.ModelMapping["deepseek-flash"])
			require.Equal(t, "glm-5.3", override.ModelMapping["glm-5.3"])
			require.Equal(t, "deepseek-v4-pro-0813", override.ModelMapping["deepseek-v4-pro-0813"])
			require.NotContains(t, override.ModelMapping, "kimi-k2.6")
			require.NotContains(t, override.ModelMapping, "glm-4.5-air")
			require.NotContains(t, override.ModelMapping, "glm-4.7")
			require.NotContains(t, override.ModelMapping, "deepseek-v4-flash")
			require.NotContains(t, override.ModelMapping, "ernie-5.0")
			scope := "account_override:" + normalizeAccountModelMappingOverrideScope(
				PlatformNewAPI, newapiconstant.ChannelTypeBaiduV2, newapiintegration.QianfanTokenPlanBaseURL,
			)
			require.ElementsMatch(t, qianfanTokenPlanForbiddenModelMappingKeys, floor.ForbiddenModelMappingKeys[scope])
		}
	}
	require.True(t, foundAli, "Ali Token Plan account override must be exported in bundle floor")
	require.True(t, foundQFPlan, "Qianfan Token Plan account override must be exported in bundle floor")
}
