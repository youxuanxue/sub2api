package service

import (
	"context"
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
	require.Contains(t, want, "deepseek-v4-flash-0731")
	require.Contains(t, want, "deepseek-v4-pro")
	require.NotContains(t, want, "qwen3-8b", "PAYG-only ch17 floor ids must not leak into Token Plan override")

	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, want, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, len(want))
	require.Equal(t, "deepseek-v4-flash-0731", mapping["deepseek-v4-flash-0731"])

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
			require.Contains(t, override.ModelMapping, "deepseek-v4-flash-0731")
			require.Contains(t, override.ModelMapping, "wan2.7-image")
			require.Contains(t, override.ModelMapping, "wan2.7-image-pro")
			require.Contains(t, override.ModelMapping, "qwen-audio-3.0-tts-plus")
			require.NotContains(t, override.ModelMapping, "qwen3-8b")
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
