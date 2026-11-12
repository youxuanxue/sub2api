package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
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

func TestAntigravityFloorKeepsNanoBanana2Alias(t *testing.T) {
	t.Parallel()
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "gemini-3.1-flash-image", mapping["nano-banana-2"])
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
	requireOpenAIImageCompatibilityAliases(t, mapping)
	requireOpenAITextCompatibilityAliases(t, mapping)
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
	} {
		// Scheduler admits aliases via CanonicalizeOpenAICompatRoutingModel before
		// IsModelSupported; mapping lookup itself only strips effort/spelling.
		routed := CanonicalizeOpenAICompatRoutingModel(model)
		require.True(t, account.IsModelSupported(routed), "known routing alias %q (routed %q) should match the OpenAI floor", model, routed)
	}
	require.NotContains(t, mapping, "gpt-5.3-codex-spark")
	require.False(t, account.IsModelSupported("gpt-5.3-codex-spark"), "spark is no longer on the OpenAI floor")
	require.False(t, account.IsModelSupported(CanonicalizeOpenAICompatRoutingModel("gpt-5.3-codex")), "legacy codex id folds to retired spark")
	require.False(t, account.IsModelSupported(CanonicalizeOpenAICompatRoutingModel("gpt-5-codex")), "legacy GPT-5 Codex id folds to retired spark")
	require.False(t, account.IsModelSupported(CanonicalizeOpenAICompatRoutingModel("codex-mini-latest")), "codex-mini-latest folds to retired spark")
	require.False(t, account.IsModelSupported(CanonicalizeOpenAICompatRoutingModel("gpt-5.3-chat-latest")), "gpt-5.3-chat-latest folds to retired spark")
	require.True(t, account.IsModelSupported("gpt-6-astra"), "gpt-6-astra must be on the OpenAI floor")
	require.True(t, account.IsModelSupported("gpt-6-sol"), "gpt-6-sol must be on the OpenAI floor")
	require.True(t, account.IsModelSupported("gpt-6-luna"), "gpt-6-luna must be on the OpenAI floor")
	require.Equal(t, "gpt-6-astra", mapping["gpt-6"], "public gpt-6 must be a floor alias onto Astra")
	require.True(t, account.IsModelSupported(CanonicalizeOpenAICompatRoutingModel("gpt-6")), "public gpt-6 alias must route onto the floor")
	require.False(t, account.IsModelSupported("gpt-not-a-real-id-zzz"), "unknown OpenAI ids must stay out of the floor")
}

func TestAccountModelMappingFloorForOps_ExportsAinzyRelayScope(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	ainzy, ok := doc.Platforms[accountModelMappingPlatformOpenAIAinzyRelay]
	require.True(t, ok)
	requireIdentityMappingForIDs(t, ainzy, supportedCatalogModelIDsFromMap(supportedOpenAIAinzyRelayCatalogModels))
	_, hasOpenAITokensea := doc.Platforms["openai_tokensea_relay"]
	require.False(t, hasOpenAITokensea)
	_, hasAnthropicTokensea := doc.Platforms["anthropic_tokensea_relay"]
	require.False(t, hasAnthropicTokensea)
	cloudwise, ok := doc.Platforms[accountModelMappingPlatformOpenAICloudwiseRelay]
	require.True(t, ok)
	require.Equal(t, openAICloudwiseRelayWildcardModelMappingFloor(), cloudwise)
	canonical, ok := doc.Platforms[PlatformOpenAI]
	require.True(t, ok)
	requireIdentityMappingForIDs(t, canonical, supportedCatalogModelIDsForPlatform(PlatformOpenAI))
}

func TestTypedTokenseaURLUsesCanonicalPlatformFloor(t *testing.T) {
	t.Parallel()
	typed := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://agent.tokensea.ai/v1",
		},
	}
	mapping, ok := accountModelMappingForAccount(context.Background(), typed, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, openAICanonicalAccountModelMappingFloor(context.Background(), nil, nil), mapping)
}

func TestAccountModelMappingFloorForOps_ExportsVertexSharedAndProfileFloors(t *testing.T) {
	t.Parallel()

	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	shared := doc.NewAPIChannelTypes["41"]
	require.Equal(t, vertexSharedModelMappingPreset(), shared)
	require.Equal(t, vertexCapabilityProfileMappingsForOps(), doc.VertexCapabilityProfiles)
	for profile, mapping := range doc.VertexCapabilityProfiles {
		for id, target := range shared {
			require.Equal(t, target, mapping[id], "profile %s must contain shared model %s", profile, id)
		}
	}
}

func TestAccountModelMappingFloorForOps_ExportsPolicyMetadata(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	require.ElementsMatch(t, canonicalAntigravityModelScopes, doc.AntigravityScopes)
	wantForbiddenKeys := append(
		append(domain.AntigravityStructuralDeadModelMappingKeys(), domain.GeminiProImageModelIDs()...),
		domain.AntigravityUnpricedModelMappingKeys()...,
	)
	sort.Strings(wantForbiddenKeys)
	require.ElementsMatch(t, slices.Compact(wantForbiddenKeys), doc.ForbiddenModelMappingKeys[PlatformAntigravity])
	require.ElementsMatch(t, kiroExclusiveModelIDs(), doc.ForbiddenModelMappingKeys[PlatformAnthropic])
	require.Contains(t, doc.ForbiddenModelMappingKeys[PlatformAnthropic], "claude-opus-5")
	require.Contains(t, doc.ForbiddenModelMappingKeys[PlatformAnthropic], "claude-opus-5-5")
	require.Contains(t, doc.ForbiddenModelMappingKeys[PlatformAnthropic], "claude-sonnet-5-5")
	require.Contains(t, doc.ForbiddenModelMappingPrefixes[PlatformAntigravity], "gpt-oss-")
	require.Contains(t, doc.ForbiddenModelMappingPrefixes[PlatformNewAPI], "imagen-")
	_, hasTokenseaForbidden := doc.ForbiddenModelMappingKeys["openai_tokensea_relay"]
	require.False(t, hasTokenseaForbidden)
}

func TestAccountModelMappingFloorForOps_ExportsAccountOverrides(t *testing.T) {
	t.Parallel()
	doc, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)

	expectedAccounts := accountModelMappingOverrideAccounts()
	require.Len(t, doc.AccountOverrides, len(expectedAccounts))
	for _, account := range expectedAccounts {
		require.Zero(t, account.ID, "property-scoped overrides must not carry an account-id selector")
		baseURL := normalizeAccountModelMappingOverrideBaseURL(account.GetBaseURL())
		var override AccountModelMappingOverride
		var found bool
		for _, candidate := range doc.AccountOverrides {
			if candidate.Platform == account.Platform &&
				candidate.ChannelType == account.ChannelType &&
				candidate.BaseURL == baseURL {
				override = candidate
				found = true
				break
			}
		}
		require.True(t, found, "missing account override for %s/%d/%s", account.Platform, account.ChannelType, baseURL)
		require.Equal(t, account.Platform, override.Platform)
		require.Equal(t, account.ChannelType, override.ChannelType)
		require.Equal(t, baseURL, override.BaseURL)

		mapping, mappingOK := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
		require.True(t, mappingOK)
		require.Equal(t, mapping, override.ModelMapping)
		require.NotEqual(t,
			doc.NewAPIChannelTypes[fmt.Sprintf("%d", account.ChannelType)],
			override.ModelMapping,
			"account override must remain narrower than its shared channel floor",
		)
	}
}

func TestAccountModelMappingFloorForOps_RuntimeDoesNotShadowVertexProfiles(t *testing.T) {
	t.Parallel()

	compiled, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	runtime, err := AccountModelMappingFloorForOps(
		context.Background(),
		`{"newapi_channel_types":{"41":{"runtime-model":"runtime-target"}}}`,
	)
	require.NoError(t, err)
	require.Equal(t, compiled.NewAPIChannelTypes["41"], runtime.NewAPIChannelTypes["41"])
	require.Equal(t, compiled.VertexCapabilityProfiles, runtime.VertexCapabilityProfiles)
}

func TestAccountModelMappingFloorForOps_RuntimeDoesNotShadowAccountOverrides(t *testing.T) {
	t.Parallel()
	compiled, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	runtime, err := AccountModelMappingFloorForOps(
		context.Background(),
		`{"newapi_channel_types":{"45":{"runtime-model":"runtime-target"}}}`,
	)
	require.NoError(t, err)
	require.Equal(t, compiled.AccountOverrides, runtime.AccountOverrides)
	require.Equal(t,
		map[string]string{"runtime-model": "runtime-target"},
		runtime.NewAPIChannelTypes["45"],
	)
}

func TestModelSurfaceBundleForOps_DigestCoversCompleteFloor(t *testing.T) {
	t.Parallel()
	bundle, err := ModelSurfaceBundleForOps(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, ModelSurfaceBundleSchemaVersion, bundle.SchemaVersion)
	require.NotNil(t, bundle.AccountModelMapping)
	raw, err := json.Marshal(bundle.AccountModelMapping)
	require.NoError(t, err)
	var projection any
	require.NoError(t, json.Unmarshal(raw, &projection))
	payload, err := json.Marshal(projection)
	require.NoError(t, err)
	digest := sha256.Sum256(payload)
	require.Equal(t, fmt.Sprintf("%x", digest), bundle.FloorSHA256)
}

func TestModelSurfaceBundleForOps_RuntimeScopeIsFullReplacement(t *testing.T) {
	t.Parallel()
	compiled, err := ModelSurfaceBundleForOps(context.Background(), "")
	require.NoError(t, err)
	runtime, err := ModelSurfaceBundleForOps(
		context.Background(),
		`{"platforms":{"grok":{"test-runtime-model":"test-runtime-target"}}}`,
	)
	require.NoError(t, err)
	require.NotEqual(t, compiled.FloorSHA256, runtime.FloorSHA256)
	require.Equal(t,
		map[string]string{"test-runtime-model": "test-runtime-target"},
		runtime.AccountModelMapping.Platforms[PlatformGrok],
	)
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

func TestGrokAccountModelMappingFloor_AppliesCompatibilityAliasesOverIdentity(t *testing.T) {
	t.Parallel()
	mapping := grokAccountModelMappingFloor(context.Background(), nil, nil)
	requireGrokDisplayBackedCompatibilityAliases(t, mapping)
}

func requireGrokDisplayBackedCompatibilityAliases(t *testing.T, mapping map[string]string) {
	t.Helper()
	expected := grokDisplayBackedCompatibilityAliases()
	require.NotEmpty(t, expected, "Grok alias SSOT must include display-listed compatibility aliases")
	for from, to := range expected {
		require.Equal(t, to, mapping[from], "display-listed Grok alias %s must map through compatibility SSOT", from)
	}
}

func requireOpenAIImageCompatibilityAliases(t *testing.T, mapping map[string]string) {
	t.Helper()
	displaySet := stringSet(supportedCatalogModelIDsForPlatform(PlatformOpenAI))
	for from, to := range tkOpenAIImageCompatibilityAliases {
		if _, ok := displaySet[to]; !ok {
			continue
		}
		require.Equal(t, to, mapping[from], "OpenAI image alias %s must map to %s", from, to)
		owner, declared := tkPricingRegistryAliasOwner(from)
		require.Truef(t, declared, "image alias %s must be an overlay _aliases key", from)
		require.Equalf(t, to, owner, "image alias %s must match overlay _aliases", from)
	}
	require.Equal(t, "gpt-image-2", mapping["gpt-image-2"])
	require.Equal(t, "gpt-image-2.5-flare", mapping["gpt-image-2.5-flare"])
	require.Equal(t, "gpt-image-2.5-sunburst", mapping["gpt-image-2.5-sunburst"])
	require.Equal(t, "gpt-image-2.5-flare", mapping["gpt-image-2.5"])
	for _, id := range []string{
		"gpt-image-2-plus",
		"gpt-image-2-pro",
		"gpt-image-2-vip",
		"gpt-image-2.5-vip",
		"gpt-image-2.5-flare-vip",
		"gpt-image-2.5-sunburst-vip",
	} {
		_, ok := mapping[id]
		require.False(t, ok, "marketing SKU %s must not be on the OpenAI floor", id)
	}
	_, hasImage25 := mapping["image-2.5"]
	require.False(t, hasImage25, "image-2.5 is not a client-facing images alias")
}

func requireOpenAITextCompatibilityAliases(t *testing.T, mapping map[string]string) {
	t.Helper()
	displaySet := stringSet(supportedCatalogModelIDsForPlatform(PlatformOpenAI))
	for from, to := range tkOpenAITextCompatibilityAliases {
		if _, ok := displaySet[to]; !ok {
			continue
		}
		require.Equal(t, to, mapping[from], "OpenAI text alias %s must map to %s", from, to)
		owner, declared := tkPricingRegistryAliasOwner(from)
		require.Truef(t, declared, "floor alias %s must be an overlay _aliases key", from)
		require.Equalf(t, to, owner, "floor alias %s must match overlay _aliases", from)
		require.Equalf(t, to, codexModelMap[from], "floor alias %s must match codexModelMap", from)
	}
	require.Equal(t, "gpt-5.6-sol", mapping["gpt-5.6"])
	require.Equal(t, "gpt-6-astra", mapping["gpt-6"])
	_, hasBareGPT6Catalog := displaySet["gpt-6"]
	require.False(t, hasBareGPT6Catalog, "bare gpt-6 stays alias-only; Sol/Luna/Astra are the catalog rows")
}

func grokDisplayBackedCompatibilityAliases() map[string]string {
	displaySet := stringSet(supportedCatalogModelIDsForPlatform(PlatformGrok))
	out := make(map[string]string)
	add := func(from, to string) {
		if from == to {
			return
		}
		if _, ok := displaySet[from]; !ok {
			return
		}
		if _, ok := displaySet[to]; ok {
			out[from] = to
		}
	}
	for from, to := range xai.DefaultModelMapping() {
		add(from, to)
	}
	for from, to := range tkGrokCompatibilityAliases {
		add(from, to)
	}
	return out
}

func requireIdentityMappingForIDs(t *testing.T, mapping map[string]string, ids []string) {
	t.Helper()
	require.NotEmpty(t, ids, "SSOT id list must be populated")
	idSet := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
		if alias, folded := tkOpenAITextCompatibilityAliases[id]; folded && mapping[id] == alias {
			continue
		}
		require.Equal(t, id, mapping[id], "mapping for SSOT id %s must be identity", id)
	}
	// Compatibility aliases may add extra keys. A catalog id may also be a
	// public alias (bare gpt-5.6 → sol). Every other SSOT key stays identity,
	// and every target stays in the SSOT id set.
	for from, to := range mapping {
		_, isSSOT := idSet[from]
		require.Contains(t, idSet, to, "mapping target %q for %q must be an SSOT id", to, from)
		if isSSOT {
			if alias, folded := tkOpenAITextCompatibilityAliases[from]; folded && to == alias {
				continue
			}
			require.Equal(t, from, to, "SSOT key %q must remain identity", from)
		}
	}
}

func modelMappingToAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
