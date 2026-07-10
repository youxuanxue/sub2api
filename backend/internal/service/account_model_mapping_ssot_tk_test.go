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
	requireIdentityMappingForIDs(t, mapping, supportedCatalogModelIDsFromMap(supportedOpenAIAinzyRelayCatalogModels))
}

func TestOpenAICanonicalFloorUsesServableOpenAIAllowlist(t *testing.T) {
	t.Parallel()
	mapping := openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil)
	requireIdentityMappingForIDs(t, mapping, supportedCatalogModelIDsForPlatform(PlatformOpenAI))
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
	require.False(t, account.IsModelSupported("gpt-not-a-real-id-zzz"), "unknown OpenAI ids must stay out of the floor")
}

func TestAccountModelMappingFloorForOps_ExportsAinzyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	ainzy, ok := doc.Platforms[accountModelMappingPlatformOpenAIAinzyRelay]
	require.True(t, ok)
	requireIdentityMappingForIDs(t, ainzy, supportedCatalogModelIDsFromMap(supportedOpenAIAinzyRelayCatalogModels))
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	requireIdentityMappingForIDs(t, canonical, supportedCatalogModelIDsForPlatform(PlatformOpenAI))
}

func TestAccountModelMappingFloorForOps_ExportsPolicyMetadata(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"claude", "gemini_text", "gemini_image"}, doc.AntigravityScopes)
	require.Contains(t, doc.ForbiddenModelMappingKeys[PlatformAntigravity], "gemini-3-pro-high")
	require.Contains(t, doc.ForbiddenModelMappingKeys[PlatformAntigravity], "tab_flash_lite_preview")
	require.Contains(t, doc.ForbiddenModelMappingPrefixes[PlatformAntigravity], "gpt-oss-")
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
	requireIdentityMappingForIDs(t, mapping, supportedCatalogModelIDsFromMap(supportedOpenAIAinzyRelayCatalogModels))
}

func requireIdentityMappingForIDs(t *testing.T, mapping map[string]string, ids []string) {
	t.Helper()
	require.NotEmpty(t, ids, "SSOT id list must be populated")
	require.Len(t, mapping, len(ids), "mapping must contain exactly the SSOT ids")
	for _, id := range ids {
		require.Equal(t, id, mapping[id], "mapping for %s must be identity", id)
	}
}
