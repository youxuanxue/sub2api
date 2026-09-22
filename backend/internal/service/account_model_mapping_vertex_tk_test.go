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
	require.Equal(t, "gemini-3.6-flash", mapping["gemini-3.5-flash-lite"])
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
