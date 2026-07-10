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
	require.Len(t, mapping, 4)
	for _, model := range []string{
		"gpt-5.3-codex-spark",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.5",
	} {
		require.Contains(t, mapping, model)
	}
	require.NotContains(t, mapping, "codex-auto-review")
	require.NotContains(t, mapping, "gpt-5-pro")
	require.NotContains(t, mapping, "gpt-5-codex")
	require.NotContains(t, mapping, "gpt-5.3-codex")
}

func TestOpenAICanonicalFloorUsesServableOpenAIAllowlist(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	require.Len(t, mapping, 4)
	for _, model := range []string{
		"gpt-5.3-codex-spark",
		"gpt-5.4",
		"gpt-5.4-mini",
		"gpt-5.5",
	} {
		require.Contains(t, mapping, model)
	}
	require.NotContains(t, mapping, "codex-auto-review")
	require.NotContains(t, mapping, "gpt-5")
	require.NotContains(t, mapping, "gpt-5-pro")
	require.NotContains(t, mapping, "gpt-5.5-pro")
	require.NotContains(t, mapping, "gpt-5.6-sol")
	require.NotContains(t, mapping, "gpt-image-1")
	require.NotContains(t, mapping, "gpt-5.2")
	require.NotContains(t, mapping, "gpt-5-codex")
}

func TestOpenAICanonicalFloorAcceptsKnownRoutingAliases(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	account := &Account{
		Platform: PlatformOpenAI,
		Credentials: map[string]any{
			"model_mapping": modelMappingToAny(mapping),
		},
	}
	for _, model := range []string{
		"gpt-5-chat-latest",
		"gpt-5-mini",
		"gpt-5.4-high",
		"gpt-5.5-pro",
		"codex-mini-latest",
		"gpt-5.3-chat-latest",
	} {
		require.True(t, account.IsModelSupported(model), "known routing alias should match the OpenAI floor")
	}
	require.True(t, account.IsModelSupported("gpt-5.3-codex-spark"), "spark itself remains served")
	require.True(t, account.IsModelSupported("gpt-5.3-codex"), "legacy codex id should alias to spark without display")
	require.True(t, account.IsModelSupported("gpt-5-codex"), "legacy GPT-5 Codex id should alias to spark without display")
	require.False(t, account.IsModelSupported("gpt-5.6"), "unsupported upstream-rejected family must stay out of the floor")
}

func TestAccountModelMappingFloorForOps_ExportsAinzyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	ainzy, ok := doc.Platforms[accountModelMappingPlatformOpenAIAinzyRelay]
	require.True(t, ok)
	require.Len(t, ainzy, 4)
	require.Contains(t, ainzy, "gpt-5.4-mini")
	require.NotContains(t, ainzy, "gpt-5-pro")
	require.NotContains(t, ainzy, "gpt-5.2")
	require.NotContains(t, ainzy, "codex-auto-review")
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	require.Len(t, canonical, 4)
	require.Contains(t, canonical, "gpt-5.3-codex-spark")
	require.NotContains(t, canonical, "gpt-5-pro")
	require.NotContains(t, canonical, "codex-auto-review")
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
	require.Len(t, mapping, 4)
	require.Contains(t, mapping, "gpt-5.4-mini")
	require.NotContains(t, mapping, "codex-auto-review")
}
