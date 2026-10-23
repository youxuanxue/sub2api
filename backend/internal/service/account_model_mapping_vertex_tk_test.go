package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestVertexSharedMappingIncludesTrafficAliases(t *testing.T) {
	t.Parallel()
	require.True(t, vertexSharedModelMappingKeysMatchIDs(),
		"vertexSharedModelMappingIDs and vertexSharedModelMapping keys must stay identical")
	mapping := vertexSharedModelMappingPreset()
	require.Equal(t, "gemini-3.8-flash", mapping["gemini-3-flash-preview"])
	require.Equal(t, "gemini-3.8-flash", mapping["gemini-3-flash"])
	// 2026-10-23 退役：3.6-flash 与其唯一别名 3.5-flash-lite 已移出请求面。
	// 钉住「移除」而不是删掉断言——否则下次上游合并会把别名悄悄带回来。
	require.NotContains(t, mapping, "gemini-3.6-flash")
	require.NotContains(t, mapping, "gemini-3.5-flash-lite")
	require.Equal(t, "gemini-embedding-001", mapping["gemini-embedding-001"])
	require.Len(t, mapping, len(vertexSharedModelMappingIDs))
}

func TestVertexCapabilityProfilesPartitionPublicUnion(t *testing.T) {
	t.Parallel()
	shared := vertexSharedModelMappingPreset()
	publicIDs := NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi)
	public := stringSet(publicIDs)
	served := map[string]struct{}{}
	for _, mapping := range vertexCapabilityProfileMappingsForOps() {
		for from, to := range shared {
			require.Equal(t, to, mapping[from])
		}
		for from, to := range mapping {
			require.Contains(t, public, from)
			require.Contains(t, public, to)
			served[from] = struct{}{}
		}
	}
	keys := make([]string, 0, len(served))
	for k := range served {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, publicIDs, keys)
}

func TestVertexCapabilityProfileSelectionIsCh41OnlyAndFailsSafe(t *testing.T) {
	t.Parallel()
	known := &Account{
		Platform: PlatformNewAPI, ChannelType: newapiconstant.ChannelTypeVertexAi,
		Credentials: map[string]any{VertexCapabilityProfileCredentialKey: " CORE-PRO "},
	}
	require.Equal(t, vertexCapabilityProfileCorePro, known.VertexCapabilityProfile())
	mapping, ok := accountModelMappingForAccount(context.Background(), known, nil, nil, nil)
	require.True(t, ok)
	want, profileKnown := vertexCapabilityProfileModelMapping(vertexCapabilityProfileCorePro)
	require.True(t, profileKnown)
	require.Equal(t, want, mapping)
	mapping, ok = accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformNewAPI, ChannelType: newapiconstant.ChannelTypeVertexAi}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, vertexSharedModelMappingPreset(), mapping)
}

// Vertex 位置决策的四条规则，两个方向对称。旧实现是一张只能「推向 global」的
// 单向清单，且优先级高于账号配置，于是既无法把 veo/embedding 这类没有 global
// 端点的模型拉回区域（现网活账号 location=global，照发必 404），又无条件剥夺了
// 运维对普通模型的选区能力。
func TestVertexLocationRespectsModelAvailabilityBothWays(t *testing.T) {
	t.Parallel()
	acct := func(loc string) *Account {
		c := map[string]any{"project_id": "p"}
		if loc != "" {
			c["location"] = loc
		}
		return &Account{Credentials: c}
	}

	// global-only：账号配了区域也必须走 global，否则 404。
	for _, m := range []string{"gemini-3-flash-preview", "gemini-3.1-pro-preview", "gemini-3-pro-image"} {
		require.Equal(t, "global", acct("us-east4").VertexLocation(m), m)
		require.Equal(t, "global", acct("").VertexLocation(m), m)
	}

	// 没有 global 端点：账号配了 global 也必须拉回区域，否则 404。
	require.Equal(t, "us-central1", acct("global").VertexLocation("gemini-embedding-001"))
	for _, m := range []string{"veo-3.1-generate-001", "veo-3.1-fast-generate-001", "veo-3.1-lite-generate-001"} {
		require.Equal(t, "us-central1", acct("global").VertexLocation(m), m)
	}
	// 但账号配的其他区域仍然听它的（这些模型在多区域可用）。
	require.Equal(t, "us-east4", acct("us-east4").VertexLocation("gemini-embedding-001"))

	// 两者皆可的普通模型：听账号配置，代码不替运维猜。
	require.Equal(t, "global", acct("global").VertexLocation("gemini-3.8-flash"))
	require.Equal(t, "us-east4", acct("us-east4").VertexLocation("gemini-3.8-flash"))

	// 逐模型显式覆盖优先于以上全部推断。
	explicit := &Account{Credentials: map[string]any{
		"project_id": "p", "location": "global",
		"vertex_model_locations": map[string]any{"gemini-embedding-001": "europe-west4"},
	}}
	require.Equal(t, "europe-west4", explicit.VertexLocation("gemini-embedding-001"))
}
