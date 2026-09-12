//go:build unit

package service

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestAccountModelMappingForAccount_AntigravityLiveClaudeSubset(t *testing.T) {
	t.Parallel()

	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	servable := supportedCatalogModelIDsForPlatform(PlatformAntigravity)
	servableSet := stringSet(servable)
	expected := expectedAntigravityModelMappingForReconcilerTest()
	require.Equal(t, expected, mapping, "Antigravity floor must be the complete owner-derived projection")
	for from, to := range mapping {
		_, fromServable := servableSet[from]
		_, toServable := servableSet[to]
		require.True(t, fromServable || toServable, "mapping %s -> %s must be anchored in Antigravity SSOT", from, to)
		require.False(t, strings.HasPrefix(from, "gpt-oss-"), "gpt-oss must not enter Antigravity model_mapping")
	}
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
		offPlatform := firstIDOutsideSetForReconcilerTest(t, supportedCatalogModelIDsForPlatform(platform), servableSet)
		require.NotContains(t, mapping, offPlatform)
	}
	require.NotContains(t, mapping, "gpt-oss-120b-medium")
}

func TestAccountModelMappingForAccount_GrokAppliesCompatibilityAliases(t *testing.T) {
	t.Parallel()

	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformGrok}, nil, nil, nil)
	require.True(t, ok)
	requireGrokDisplayBackedCompatibilityAliases(t, mapping)
}

func TestAccountModelMappingForAccount_NativePlatformsExplicit(t *testing.T) {
	t.Parallel()

	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformGemini} {
		platform := platform
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: platform}, nil, nil, nil)
			require.True(t, ok)
			requireIdentityMappingForIDs(t, mapping, supportedCatalogModelIDsForPlatform(platform))
			require.NotContains(t, mapping, platform+"-not-a-real-id-zzz")
		})
	}
}

func TestAccountModelMappingForAccount_KiroBedrockAndNewAPI(t *testing.T) {
	t.Parallel()

	kiro, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformKiro}, nil, nil, nil)
	require.True(t, ok)
	requireIdentityMappingForIDs(t, kiro, kiroModelMappingPresetIDs())
	require.NotContains(t, kiro, "claude-not-kiro-zzz")

	kiroStub, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Name:     "kiro-us6",
	}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, kiro, kiroStub)

	bedrock, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeBedrock,
	}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, domain.DefaultBedrockModelMapping, bedrock)

	vertex, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
	}, nil, nil, nil)
	require.True(t, ok)
	requireIdentityMappingForIDs(t, vertex, vertexSharedModelMappingPresetIDs())
}

func TestAccountModelMappingRuntimeOverride(t *testing.T) {
	t.Parallel()

	grokID := firstStringSortedForReconcilerTest(t, supportedCatalogModelIDsForPlatform(PlatformGrok))
	anthropicID := firstStringSortedForReconcilerTest(t, supportedCatalogModelIDsForPlatform(PlatformAnthropic))
	vertexID := "runtime-only-vertex-model"
	raw := runtimeOverrideRawForReconcilerTest(t, accountModelMappingRuntimeDoc{
		Platforms: map[string]map[string]string{
			"grok":   {grokID: grokID},
			"claude": {anthropicID: anthropicID},
		},
		NewAPIChannelTypes: map[string]map[string]string{
			strconv.Itoa(newapiconstant.ChannelTypeVertexAi): {vertexID: vertexID},
		},
	})
	runtime, err := parseAccountModelMappingRuntime(raw)
	require.NoError(t, err)

	grok, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformGrok}, nil, nil, runtime)
	require.True(t, ok)
	require.Equal(t, map[string]string{grokID: grokID}, grok)

	anthropic, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAnthropic}, nil, nil, runtime)
	require.True(t, ok)
	require.Equal(t, map[string]string{anthropicID: anthropicID}, anthropic)

	vertex, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
	}, nil, nil, runtime)
	require.True(t, ok)
	requireIdentityMappingForIDs(t, vertex, vertexSharedModelMappingPresetIDs())
	require.NotContains(t, vertex, vertexID,
		"runtime channel replacement must not erase the ch41 profile/shared capability contract")
}

func TestReconciledAccountModelMapping_RemovesForbiddenEntries(t *testing.T) {
	t.Parallel()

	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{"model_mapping": map[string]any{
			"required":                          "wrong-target",
			"compatible-extra":                  "compatible-extra",
			"gpt-oss-forbidden-prefix-boundary": "gpt-oss-forbidden-prefix-boundary",
		}},
	}
	got := reconciledAccountModelMapping(account, map[string]string{"required": "required-target"})
	require.Equal(t, "required-target", got["required"])
	require.Equal(t, "compatible-extra", got["compatible-extra"])
	require.NotContains(t, got, "gpt-oss-forbidden-prefix-boundary")
}

func expectedAntigravityModelMappingForReconcilerTest() map[string]string {
	servable := stringSet(supportedCatalogModelIDsForPlatform(PlatformAntigravity))
	expected := make(map[string]string)
	for from, to := range domain.DefaultAntigravityModelMapping {
		if strings.HasPrefix(from, "gpt-oss-") ||
			domain.IsAntigravityStructuralDeadModelMappingKey(from) ||
			domain.IsAntigravityUnpricedModelMappingKey(from) {
			continue
		}
		if _, ok := servable[from]; ok {
			expected[from] = to
			continue
		}
		if _, ok := servable[to]; ok {
			expected[from] = to
		}
	}
	return expected
}

func firstIDOutsideSetForReconcilerTest(t *testing.T, candidates []string, excluded map[string]struct{}) string {
	t.Helper()
	for _, id := range candidates {
		if _, ok := excluded[id]; !ok {
			return id
		}
	}
	require.FailNow(t, "expected at least one candidate outside excluded set")
	return ""
}

func firstStringSortedForReconcilerTest(t *testing.T, ids []string) string {
	t.Helper()
	require.NotEmpty(t, ids, "SSOT sample source must be populated")
	sorted := append([]string{}, ids...)
	sort.Strings(sorted)
	return sorted[0]
}

func runtimeOverrideRawForReconcilerTest(t *testing.T, doc accountModelMappingRuntimeDoc) string {
	t.Helper()
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(raw)
}
