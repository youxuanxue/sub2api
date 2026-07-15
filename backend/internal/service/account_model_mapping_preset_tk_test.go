package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestAccountModelMappingPresetIDs_GrokUsesServableCatalog(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformGrok, 0, nil)
	require.ElementsMatch(t, supportedCatalogModelIDsForPlatform(PlatformGrok), ids)
}

func TestAccountModelMappingPresetIDs_KiroUsesAdminTestModels(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformKiro, 0, nil)
	require.ElementsMatch(t, kiroAdminTestModelIDsForPresetTest(), ids)
}

func TestAccountModelMappingPresetIDs_NewAPIVertexMatchesGeminiServable(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeVertexAi, nil)
	require.ElementsMatch(t, supportedCatalogModelIDsForPlatform(PlatformGemini), ids)
}

func TestAccountModelMappingPresetIDs_NewAPIOtherChannelEmpty(t *testing.T) {
	t.Parallel()
	// Moonshot (25) is not in tk_served_models manifest — no TK-verified preset.
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, 25, nil)
	require.Empty(t, ids)
}

func TestAccountModelMappingPresetIDs_NewAPIDeepSeekUsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeDeepSeek, nil)
	require.ElementsMatch(t, tkServedModelsManifestPresetIDsByChannelType(newapiconstant.ChannelTypeDeepSeek), ids)
}

func TestAccountModelMappingPresetIDs_NewAPIAliUsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeAli, nil)
	require.ElementsMatch(t, tkServedModelsManifestPresetIDsByChannelType(newapiconstant.ChannelTypeAli), ids)
}

func TestAccountModelMappingPresetIDs_NewAPIZhipuV4EmptyAfterDirectPoolRemoval(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeZhipu_v4, nil)
	require.Empty(t, ids, "GLM direct account/group was removed; GLM display intent now rides Qwen/China pools")

	overrideIDs, managed := NewAPIModelMappingPresetOverrideIDsForChannelType(newapiconstant.ChannelTypeZhipu_v4)
	require.True(t, managed, "removed direct GLM channel must still override stale new-api defaults")
	require.Empty(t, overrideIDs)
	require.Contains(t, NewAPIModelMappingPresetOverrideChannelTypes(), newapiconstant.ChannelTypeZhipu_v4)
}

func TestNewAPIModelDisplayIDsForChannelType_UsesDisplayProjection(t *testing.T) {
	t.Parallel()
	channelType := firstManifestPresetChannelTypeForPresetTest(t)
	adminIDs := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, channelType, nil)
	displayIDs := NewAPIModelDisplayIDsForChannelType(channelType)
	require.ElementsMatch(t, tkServedModelsManifestPresetIDsByChannelType(channelType), adminIDs)
	require.ElementsMatch(t, tkServedModelsManifestDisplayPresetIDsByChannelType(channelType), displayIDs)
	require.Subset(t, adminIDs, displayIDs, "display projection must not invent ids outside admin provisioning intent")
}

func TestAccountModelMappingPresetIDs_NewAPIVolcEngineUsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeVolcEngine, nil)
	require.ElementsMatch(t, tkServedModelsManifestPresetIDsByChannelType(newapiconstant.ChannelTypeVolcEngine), ids)
}

func TestAccountModelMappingPresetIDs_UnknownPlatformEmpty(t *testing.T) {
	t.Parallel()
	require.Empty(t, AccountModelMappingPresetIDs(context.Background(), "totally-unknown", 0, nil))
}

func TestAccountModelMappingPresetIDs_ClaudeAliasMatchesAnthropic(t *testing.T) {
	t.Parallel()
	require.Equal(
		t,
		AccountModelMappingPresetIDs(context.Background(), PlatformAnthropic, 0, nil),
		AccountModelMappingPresetIDs(context.Background(), "claude", 0, nil),
	)
}

func kiroAdminTestModelIDsForPresetTest() []string {
	models := KiroAdminTestModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}

func firstManifestPresetChannelTypeForPresetTest(t *testing.T) int {
	t.Helper()
	channelTypes := NewAPIManifestPresetChannelTypes()
	require.NotEmpty(t, channelTypes, "served-models manifest must expose at least one channel_type")
	return channelTypes[0]
}
