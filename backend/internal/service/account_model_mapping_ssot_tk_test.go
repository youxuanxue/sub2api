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
	require.Len(t, mapping, 9)
	for _, model := range []string{
		"codex-auto-review",
		"gpt-5-codex",
		"gpt-5.2",
		"gpt-5.2-pro",
		"gpt-5.3",
		"gpt-5.3-codex",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.5",
	} {
		require.Contains(t, mapping, model)
	}
	require.NotContains(t, mapping, "gpt-5-pro")
	require.NotContains(t, mapping, "gpt-5.3-codex-spark")
}

func TestOpenAICanonicalFloorUsesServableOpenAIAllowlist(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	require.Len(t, mapping, 20)
	for _, model := range []string{
		"codex-auto-review",
		"gpt-5",
		"gpt-5-chat",
		"gpt-5-chat-latest",
		"gpt-5-mini",
		"gpt-5-nano",
		"gpt-5-pro",
		"gpt-5-search-api",
		"gpt-5.1",
		"gpt-5.1-chat-latest",
		"gpt-5-codex",
		"gpt-5.2",
		"gpt-5.2-pro",
		"gpt-5.3",
		"gpt-5.3-codex",
		"gpt-5.3-codex-spark",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.4-pro",
		"gpt-5.5",
	} {
		require.Contains(t, mapping, model)
	}
	require.NotContains(t, mapping, "gpt-5.5-pro")
	require.NotContains(t, mapping, "gpt-5.6-sol")
	require.NotContains(t, mapping, "gpt-image-1")
}

func TestAccountModelMappingFloorForOps_ExportsAinzyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	ainzy, ok := doc.Platforms[accountModelMappingPlatformOpenAIAinzyRelay]
	require.True(t, ok)
	require.Len(t, ainzy, 9)
	require.Contains(t, ainzy, "gpt-5.2")
	require.NotContains(t, ainzy, "gpt-5-pro")
	require.NotContains(t, ainzy, "gpt-5.3-codex-spark")
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	require.Len(t, canonical, 20)
	require.Contains(t, canonical, "gpt-5.2")
	require.Contains(t, canonical, "gpt-5-pro")
	require.Contains(t, canonical, "gpt-5.3-codex-spark")
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
	require.Len(t, mapping, 9)
	require.Contains(t, mapping, "gpt-5.4-mini")
}
