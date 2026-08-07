package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func TestIsNewAPIQianfanAccount(t *testing.T) {
	t.Parallel()
	require.True(t, isNewAPIQianfanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanBaseURL,
		},
	}))
	require.False(t, isNewAPIQianfanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanBaseURL + "/v2",
		},
	}))
}

func TestNewAPIModelMappingPresetIDsForQianfanAccount(t *testing.T) {
	t.Parallel()
	account := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanBaseURL,
		},
	}
	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Equal(t, []string{
		"bge-large-en",
		"bge-large-zh",
		"deepseek-ocr",
		"deepseek-v3.2",
		"deepseek-v3.2-think",
		"deepseek-v4-flash",
		"deepseek-v4-flash-0731",
		"deepseek-v4-pro",
		"ernie-4.5-turbo-128k",
		"ernie-4.5-turbo-20260402",
		"ernie-4.5-turbo-32k",
		"ernie-4.5-turbo-vl",
		"ernie-4.5-turbo-vl-32k",
		"ernie-5.0",
		"ernie-5.0-thinking-preview",
		"ernie-5.1",
		"ernie-x1.1",
		"ernie-x1.1-preview",
		"glm-5",
		"glm-5.1",
		"glm-5.2",
		"internvl3-38b",
		"kimi-k2.6",
		"qianfan-ocr",
		"qwen3-embedding-0.6b",
		"qwen3-embedding-4b",
		"qwen3-embedding-8b",
		"qwen3.5-122b-a10b",
		"qwen3.5-27b",
		"qwen3.5-35b-a3b",
		"qwen3.5-397b-a17b",
	}, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, 31)
	require.Equal(t, "deepseek-v4-pro", mapping["deepseek-v4-pro"])
}

func TestAccountModelMappingFloorForOpsIncludesQianfanOverride(t *testing.T) {
	t.Parallel()
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	require.NotEmpty(t, floor.AccountOverrides)
	found := false
	for _, override := range floor.AccountOverrides {
		if override.ChannelType == newapiconstant.ChannelTypeBaiduV2 &&
			override.BaseURL == newapiintegration.QianfanBaseURL {
			found = true
			require.Contains(t, override.ModelMapping, "ernie-4.5-turbo-vl-32k")
			require.Contains(t, override.ModelMapping, "deepseek-v4-pro")
			require.Contains(t, override.ModelMapping, "glm-5.2")
		}
	}
	require.True(t, found, "qianfan account override must be exported in bundle floor")
}
