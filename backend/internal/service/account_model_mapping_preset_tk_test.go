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
	require.NotEmpty(t, ids)
	require.Contains(t, ids, GrokDefaultTestModelID)
	for _, id := range ids {
		require.Contains(t, supportedGrokCatalogModels, id)
	}
}

func TestAccountModelMappingPresetIDs_KiroUsesAdminTestModels(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformKiro, 0, nil)
	want := make([]string, 0, len(KiroAdminTestModels()))
	for _, m := range KiroAdminTestModels() {
		want = append(want, m.ID)
	}
	require.ElementsMatch(t, want, ids)
}

func TestAccountModelMappingPresetIDs_NewAPIVertexMatchesGeminiServable(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeVertexAi, nil)
	require.NotEmpty(t, ids)
	require.Contains(t, ids, "gemini-2.5-flash")
	require.Contains(t, ids, "imagen-4.0-fast-generate-001")
	require.Contains(t, ids, "veo-3.1-generate-001")
	require.ElementsMatch(t, VertexNewAPIChannelServableModelIDs(), ids)
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
	require.NotEmpty(t, ids)
	require.Contains(t, ids, "deepseek-chat")
	require.Contains(t, ids, "deepseek-v4-pro")
}

func TestAccountModelMappingPresetIDs_NewAPIAliUsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeAli, nil)
	require.NotEmpty(t, ids)
	require.Contains(t, ids, "qwen3.7-max")
}

func TestAccountModelMappingPresetIDs_NewAPIZhipuV4UsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeZhipu_v4, nil)
	require.NotEmpty(t, ids)
	require.Contains(t, ids, "glm-5-turbo")
	require.NotContains(t, ids, "qwen3.7-max")
}

func TestAccountModelMappingPresetIDs_NewAPIVolcEngineUsesManifest(t *testing.T) {
	t.Parallel()
	ids := AccountModelMappingPresetIDs(context.Background(), PlatformNewAPI, newapiconstant.ChannelTypeVolcEngine, nil)
	require.NotEmpty(t, ids)
	require.Contains(t, ids, "doubao-seed-2-0-pro-260215")
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
