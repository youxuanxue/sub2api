//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
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
	require.Equal(t, "claude-sonnet-4-6", mapping["claude-sonnet-4-6"])
	require.Equal(t, "claude-opus-4-6-thinking", mapping["claude-opus-4-6"])
	require.Equal(t, "claude-opus-4-6-thinking", mapping["claude-opus-4-6-thinking"])
	require.Contains(t, mapping, "gemini-3.5-flash-low")
	require.Contains(t, mapping, "gemini-3.1-flash-image")
	for _, denied := range []string{
		"claude-fable-5",
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-haiku-4-5",
		"gpt-oss-120b-medium",
		"tab_flash_lite_preview",
		"gemini-3-pro-preview",
	} {
		require.NotContains(t, mapping, denied)
	}
}

func TestAccountModelMappingForAccount_GrokPreservesAliases(t *testing.T) {
	t.Parallel()

	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformGrok}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "grok-4.3", mapping["grok"])
	require.Equal(t, "grok-4.3", mapping["grok-latest"])
	require.Equal(t, "grok-4.3", mapping["grok-4.3-latest"])
	require.Equal(t, "grok-4.3", mapping["grok-4-fast-reasoning"])
	require.Equal(t, "grok-build-0.1", mapping["grok-build"])
	require.Equal(t, "grok-build-0.1", mapping["grok-code-fast"])
	require.Equal(t, "grok-build-0.1", mapping["grok-code-fast-1-0825"])
	require.Equal(t, "grok-4.20-0309-reasoning", mapping["grok-4.20-reasoning"])
	require.Equal(t, "grok-4.20-0309-non-reasoning", mapping["grok-4.20-non-reasoning"])
	require.Contains(t, mapping, "grok-imagine-video")
}

func TestAccountModelMappingForAccount_NativePlatformsExplicit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		platform string
		want     string
		deny     string
	}{
		{PlatformAnthropic, "claude-sonnet-4-6", "claude-sonnet-4-5-20250929"},
		{PlatformOpenAI, "gpt-5.3-codex-spark", "gpt-5-pro"},
		{PlatformGemini, "gemini-2.5-flash", "gemini-2.0-flash"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.platform, func(t *testing.T) {
			t.Parallel()
			mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: tc.platform}, nil, nil, nil)
			require.True(t, ok)
			require.Equal(t, tc.want, mapping[tc.want])
			require.NotContains(t, mapping, tc.deny)
		})
	}
}

func TestAccountModelMappingForAccount_KiroBedrockAndNewAPI(t *testing.T) {
	t.Parallel()

	kiro, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformKiro}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "claude-sonnet-4-5", kiro["claude-sonnet-4-5"])
	require.Equal(t, "claude-sonnet-5", kiro["claude-sonnet-5"])
	require.NotContains(t, kiro, "claude-fable-5")

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
	require.Equal(t, "us.anthropic.claude-opus-4-8-v1", bedrock["claude-opus-4-8"])

	vertex, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
	}, nil, nil, nil)
	require.True(t, ok)
	require.Equal(t, "imagen-4.0-generate-001", vertex["imagen-4.0-generate-001"])
}

func TestAccountModelMappingRuntimeOverride(t *testing.T) {
	t.Parallel()

	raw := `{
		"platforms": {
			"grok": {"grok": "grok-4.3"},
			"claude": {"claude-sonnet-4-6": "claude-sonnet-4-6"}
		},
		"newapi_channel_types": {
			"41": {"imagen-4.0-generate-001": "imagen-4.0-generate-001"}
		}
	}`
	runtime, err := parseAccountModelMappingRuntime(raw)
	require.NoError(t, err)

	grok, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformGrok}, nil, nil, runtime)
	require.True(t, ok)
	require.Equal(t, map[string]string{"grok": "grok-4.3"}, grok)

	anthropic, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAnthropic}, nil, nil, runtime)
	require.True(t, ok)
	require.Equal(t, map[string]string{"claude-sonnet-4-6": "claude-sonnet-4-6"}, anthropic)

	vertex, ok := accountModelMappingForAccount(context.Background(), &Account{
		Platform:    PlatformNewAPI,
		ChannelType: newapiconstant.ChannelTypeVertexAi,
	}, nil, nil, runtime)
	require.True(t, ok)
	require.Equal(t, map[string]string{"imagen-4.0-generate-001": "imagen-4.0-generate-001"}, vertex)
}

func TestAccountModelMappingReconciler_RewritesDriftedAccountsAcrossPlatforms(t *testing.T) {
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformAntigravity: {
				{ID: 1, Platform: PlatformAntigravity, Credentials: nil},
			},
			PlatformGrok: {
				{ID: 2, Platform: PlatformGrok, Credentials: map[string]any{"model_mapping": map[string]any{"grok": "grok-4.3"}}},
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
	acc := &reconcilerAccountStub{
		byPlatform: map[string][]Account{
			PlatformGrok: {
				{ID: 9, Platform: PlatformGrok, Credentials: map[string]any{}},
			},
		},
	}
	settings := accountModelMappingSettingStub{values: map[string]string{
		SettingKeyTKAccountModelMappingRuntime: `{"platforms":{"grok":{"grok":"grok-4.3"}}}`,
	}}
	r := NewAccountModelMappingReconciler(acc, nil, settings, nil, nil)
	r.runOnce(context.Background())

	require.Len(t, acc.bulkCalls, 1)
	require.Equal(t, []int64{9}, acc.bulkCalls[0].ids)
	require.Equal(t, map[string]any{"grok": "grok-4.3"}, acc.bulkCalls[0].updates.Credentials["model_mapping"])
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
