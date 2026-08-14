package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/require"
)

func TestVertexCapabilityProfilesPartitionPublicUnion(t *testing.T) {
	t.Parallel()

	shared := stringSet(vertexSharedModelMappingPresetIDs())
	publicIDs := NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi)
	public := stringSet(publicIDs)
	require.NotEmpty(t, shared)
	require.NotEmpty(t, public)

	served := make(map[string]struct{}, len(public))
	for profile, mapping := range vertexCapabilityProfileMappingsForOps() {
		require.NotContains(t, profile, "47")
		require.NotContains(t, profile, "57")
		for id := range shared {
			require.Equal(t, id, mapping[id], "profile %s must contain shared model %s", profile, id)
		}
		for id, target := range mapping {
			require.Equal(t, id, target, "Vertex profile mappings are identity floors")
			require.Contains(t, public, id, "profile %s must stay inside public union", profile)
			served[id] = struct{}{}
		}
	}
	require.ElementsMatch(t, publicIDs, mapKeysForVertexProfileTest(served),
		"every public ch41 model must have a shared/profile serving path")
}

func TestVertexCapabilityProfileSelectionIsCh41OnlyAndFailsSafe(t *testing.T) {
	t.Parallel()

	known := &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
		Credentials: map[string]any{VertexCapabilityProfileCredentialKey: " CORE-PRO "},
	}
	require.Equal(t, vertexCapabilityProfileCorePro, known.VertexCapabilityProfile())
	mapping, ok := accountModelMappingForAccount(context.Background(), known, nil, nil, nil)
	require.True(t, ok)
	ids, profileKnown := vertexCapabilityProfileModelMappingIDs(vertexCapabilityProfileCorePro)
	require.True(t, profileKnown)
	requireIdentityMappingForIDs(t, mapping, ids)

	for _, account := range []*Account{
		{Platform: PlatformNewAPI, ChannelType: newapiconstant.ChannelTypeVertexAi},
		{
			Platform:    PlatformNewAPI,
			ChannelType: newapiconstant.ChannelTypeVertexAi,
			Credentials: map[string]any{VertexCapabilityProfileCredentialKey: "unknown-profile"},
		},
	} {
		mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
		require.True(t, ok)
		requireIdentityMappingForIDs(t, mapping, vertexSharedModelMappingPresetIDs())
	}

	nonVertex := &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Credentials: map[string]any{VertexCapabilityProfileCredentialKey: vertexCapabilityProfileCorePro},
	}
	require.Empty(t, nonVertex.VertexCapabilityProfile())
	native := &Account{
		Platform:    PlatformGemini,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
		Credentials: map[string]any{VertexCapabilityProfileCredentialKey: vertexCapabilityProfileCorePro},
	}
	require.Empty(t, native.VertexCapabilityProfile())
}

func mapKeysForVertexProfileTest(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}
