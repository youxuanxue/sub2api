package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_IsOpenAIAinzyRelay(t *testing.T) {
	t.Parallel()
	require.False(t, (*Account)(nil).IsOpenAIAinzyRelay())

	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, oauth.IsOpenAIAinzyRelay())

	official := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.openai.com/v1",
		},
	}
	require.False(t, official.IsOpenAIAinzyRelay())

	otherRelay := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example.com/v1",
		},
	}
	require.False(t, otherRelay.IsOpenAIAinzyRelay())

	ainzy := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.ainzy.net/v1",
		},
	}
	require.True(t, ainzy.IsOpenAIAinzyRelay())
}

func TestOpenAIAinzyRelayFloorIsProbeCuratedOnly(t *testing.T) {
	t.Parallel()
	mapping := openAIAinzyRelayAccountModelMappingFloor(context.Background(), nil, nil)
	require.Len(t, mapping, 5)
	require.Contains(t, mapping, "gpt-5.2")
	require.Contains(t, mapping, "gpt-5.3-codex")
	require.NotContains(t, mapping, "gpt-5.4-mini")
	require.NotContains(t, mapping, "gpt-5.5")
	require.NotContains(t, mapping, "codex-auto-review")
}

func TestOpenAICanonicalFloorExcludesAinzyOnlyModels(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	require.NotEmpty(t, mapping)
	require.NotContains(t, mapping, "gpt-5.2")
	require.NotContains(t, mapping, "gpt-5.3-codex")
	require.Contains(t, mapping, "gpt-5.5")
}

func TestAccountModelMappingFloorForOps_ExportsAinzyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	ainzy, ok := doc.Platforms[accountModelMappingPlatformOpenAIAinzyRelay]
	require.True(t, ok)
	require.Len(t, ainzy, 5)
	require.Contains(t, ainzy, "gpt-5.2")
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	require.NotContains(t, canonical, "gpt-5.2")
}

func TestAccountModelMappingForAccount_AinzyUsesCuratedFloor(t *testing.T) {
	t.Parallel()
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.ainzy.net/v1",
		},
	}, nil, nil, nil)
	require.True(t, ok)
	require.Len(t, mapping, 5)
}
