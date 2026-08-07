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
		"deepseek-v3.2",
		"deepseek-v3.2-think",
		"deepseek-v4-flash",
		"deepseek-v4-flash-0731",
		"deepseek-v4-pro",
		"ernie-4.5-turbo-vl-32k",
		"glm-5.2",
	}, got)

	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, 7)
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
