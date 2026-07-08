package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccount_IsOpenAIThirdPartyRelay(t *testing.T) {
	t.Parallel()
	require.False(t, (*Account)(nil).IsOpenAIThirdPartyRelay())

	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	require.False(t, oauth.IsOpenAIThirdPartyRelay())

	official := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.openai.com/v1",
		},
	}
	require.False(t, official.IsOpenAIThirdPartyRelay())

	relay := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.ainzy.net/v1",
		},
	}
	require.True(t, relay.IsOpenAIThirdPartyRelay())
}

func TestOpenAIThirdPartyRelayFloorIncludesRelayOnlyModels(t *testing.T) {
	t.Parallel()
	mapping := openAIThirdPartyRelayAccountModelMappingFloor(context.Background(), nil, nil)
	require.NotEmpty(t, mapping)
	require.Contains(t, mapping, "gpt-5.2")
	require.Contains(t, mapping, "gpt-5.3-codex")
	require.Contains(t, mapping, "gpt-5.5")
}

func TestOpenAICanonicalFloorExcludesRelayOnlyModels(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	require.NotEmpty(t, mapping)
	require.NotContains(t, mapping, "gpt-5.2")
	require.NotContains(t, mapping, "gpt-5.3-codex")
	require.Contains(t, mapping, "gpt-5.5")
}

func TestAccountModelMappingFloorForOps_ExportsThirdPartyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	relay, ok := doc.Platforms[accountModelMappingPlatformOpenAIThirdPartyRelay]
	require.True(t, ok)
	require.Contains(t, relay, "gpt-5.2")
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	require.NotContains(t, canonical, "gpt-5.2")
}
