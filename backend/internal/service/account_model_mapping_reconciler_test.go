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

type accountModelMappingGroupStub struct {
	byPlatform  map[string][]Group
	updateCalls []Group
	listErr     error
}

func (s *accountModelMappingGroupStub) ListActiveByPlatform(_ context.Context, platform string) ([]Group, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.byPlatform[platform], nil
}

func (s *accountModelMappingGroupStub) Update(_ context.Context, g *Group) error {
	s.updateCalls = append(s.updateCalls, *g)
	return nil
}

type accountModelMappingSettingStub struct {
	values map[string]string
}

func (s accountModelMappingSettingStub) GetRawSettingValue(_ context.Context, key string) (string, bool) {
	if s.values == nil {
		return "", false
	}
	v, ok := s.values[key]
	return v, ok
}

func TestAccountModelMappingForAccount_AntigravityLiveClaudeSubset(t *testing.T) {
	t.Parallel()

	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	servable := supportedCatalogModelIDsForPlatform(PlatformAntigravity)
	servableSet := stringSet(servable)
	require.NotEmpty(t, mapping)
	for from, to := range mapping {
		_, fromServable := servableSet[from]
		_, toServable := servableSet[to]
		require.True(t, fromServable || toServable, "mapping %s -> %s must be anchored in Antigravity SSOT", from, to)
		require.False(t, strings.HasPrefix(from, "gpt-oss-"), "gpt-oss must not enter Antigravity model_mapping")
	}
	from, to := firstAntigravityDefaultAliasForReconcilerTest(t, servableSet)
	require.Equal(t, to, mapping[from])
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
	requireIdentityMappingForIDs(t, vertex, NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi))
}

func TestAccountModelMappingRuntimeOverride(t *testing.T) {
	t.Parallel()

	grokID := firstStringSortedForReconcilerTest(t, supportedCatalogModelIDsForPlatform(PlatformGrok))
	anthropicID := firstStringSortedForReconcilerTest(t, supportedCatalogModelIDsForPlatform(PlatformAnthropic))
	vertexID := firstStringSortedForReconcilerTest(t, NewAPIModelDisplayIDsForChannelType(newapiconstant.ChannelTypeVertexAi))
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
	require.Equal(t, map[string]string{vertexID: vertexID}, vertex)
}

func TestAccountModelMappingReconciler_RewritesDriftedAccountsAcrossPlatforms(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{ID: 1, Platform: PlatformAntigravity, Credentials: nil},
			},
			PlatformGrok: {
				{ID: 2, Platform: PlatformGrok, Credentials: map[string]any{"model_mapping": map[string]any{"grok-not-current-zzz": "grok-not-current-zzz"}}},
			},
			PlatformKiro: {
				{ID: 3, Platform: PlatformKiro, Credentials: map[string]any{}},
			},
			PlatformOpenAI: {
				{ID: 4, Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": modelMappingToAny(identityModelMapping(supportedCatalogModelIDsForPlatform(PlatformOpenAI)))}},
			},
		},
	}
	r := NewAccountModelMappingReconciler(acc, nil, nil, nil, nil)
	r.runOnce(context.Background())

	var touched []int64
	for _, c := range acc.bulkCalls {
		touched = append(touched, c.ids...)
		mm, ok := c.updates.Credentials["model_mapping"].(map[string]any)
		require.True(t, ok)
		require.NotEmpty(t, mm)
	}
	require.ElementsMatch(t, []int64{1, 2, 3}, touched)
}

func TestAccountModelMappingReconciler_RuntimeOverrideFromSettings(t *testing.T) {
	grokID := firstStringSortedForReconcilerTest(t, supportedCatalogModelIDsForPlatform(PlatformGrok))
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformGrok: {
				{ID: 9, Platform: PlatformGrok, Credentials: map[string]any{}},
			},
		},
	}
	settings := accountModelMappingSettingStub{values: map[string]string{
		SettingKeyTKAccountModelMappingRuntime: runtimeOverrideRawForReconcilerTest(t, accountModelMappingRuntimeDoc{
			Platforms: map[string]map[string]string{
				"grok": {grokID: grokID},
			},
		}),
	}}
	r := NewAccountModelMappingReconciler(acc, nil, settings, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1)
	require.Equal(t, []int64{9}, acc.bulkCalls[0].ids)
	require.Equal(t, map[string]any{grokID: grokID}, acc.bulkCalls[0].updates.Credentials["model_mapping"])
}

func TestAccountModelMappingReconciler_AntigravityGroupScopesAllowClaudeAndGemini(t *testing.T) {
	grp := &accountModelMappingGroupStub{
		byPlatform: map[string][]Group{
			PlatformAntigravity: {
				{ID: 1, Platform: PlatformAntigravity, SupportedModelScopes: []string{"gemini_text", "gemini_image"}},
				{ID: 2, Platform: PlatformAntigravity, SupportedModelScopes: []string{"claude", "gemini_text", "gemini_image"}},
				{ID: 3, Platform: PlatformAntigravity, SupportedModelScopes: nil},
			},
		},
	}
	r := NewAccountModelMappingReconciler(nil, grp, nil, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, grp.updateCalls, 2)
	ids := []int64{grp.updateCalls[0].ID, grp.updateCalls[1].ID}
	require.ElementsMatch(t, []int64{1, 3}, ids)
	for _, g := range grp.updateCalls {
		require.ElementsMatch(t, canonicalAntigravityModelScopes, g.SupportedModelScopes)
	}
}

func TestAccountModelMappingReconciler_NilSafe(t *testing.T) {
	var nilRec *AccountModelMappingReconciler
	require.NotPanics(t, func() { nilRec.runOnce(context.Background()); nilRec.RunOnce() })

	rec := NewAccountModelMappingReconciler(nil, nil, nil, nil, nil)
	require.NotPanics(t, func() { rec.runOnce(context.Background()); rec.RunOnce() })
}

func firstAntigravityDefaultAliasForReconcilerTest(t *testing.T, servableSet map[string]struct{}) (string, string) {
	t.Helper()
	keys := make([]string, 0, len(domain.DefaultAntigravityModelMapping))
	for from := range domain.DefaultAntigravityModelMapping {
		keys = append(keys, from)
	}
	sort.Strings(keys)
	for _, from := range keys {
		to := domain.DefaultAntigravityModelMapping[from]
		if from == to {
			continue
		}
		if _, ok := servableSet[from]; ok {
			return from, to
		}
		if _, ok := servableSet[to]; ok {
			return from, to
		}
	}
	require.FailNow(t, "expected at least one Antigravity alias anchored in servable SSOT")
	return "", ""
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
