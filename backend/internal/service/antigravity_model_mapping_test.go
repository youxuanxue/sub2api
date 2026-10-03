//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestAntigravityGatewayService_GetMappedModel_ConvergedSurface(t *testing.T) {
	svc := &AntigravityGatewayService{}
	cases := map[string]string{
		"gemini-3.6-flash":       "gemini-3.6-flash-tiered",
		"gemini-3.7-flash":       "gemini-3.7-flash-high",
		"gemini-3.8-flash":       "gemini-3.8-flash-high",
		"gemini-3-flash":         "gemini-3.8-flash-high",
		"gemini-3-flash-preview": "gemini-3.8-flash-high",
		"gemini-3.5-flash-lite":  "gemini-3.6-flash-tiered",
		"gemini-3.1-flash-image": "gemini-3.1-flash-image",
		"gemini-3-pro-image":     "",
		"nano-2":                 "gemini-3.1-flash-image",
		"nano-banana-2":          "gemini-3.1-flash-image",
		"nano-pro":               "",
		"claude-sonnet-4-6":      "",
	}
	for requested, expected := range cases {
		require.Equal(t, expected, svc.getMappedModel(&Account{Platform: PlatformAntigravity}, requested), requested)
	}
}

func TestAntigravityDefaultModels_SubsetOfDefaultMapping(t *testing.T) {
	t.Parallel()
	// Public listing IDs resolve through the default mapping; Pro is absent.
	hasNanoProListing := false
	for _, m := range antigravity.DefaultModels() {
		if m.ID == "nano-pro" {
			hasNanoProListing = true
		}
		_, ok := domain.DefaultAntigravityModelMapping[m.ID]
		require.True(t, ok, "DefaultModels id %q missing from DefaultAntigravityModelMapping", m.ID)
	}
	require.False(t, hasNanoProListing, "nano-pro must not be listed on Antigravity")
	require.Empty(t, domain.DefaultAntigravityModelMapping["nano-pro"])
}

func TestMapAntigravityModel_WildcardTargetEqualsRequest(t *testing.T) {
	account := &Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"model_mapping": map[string]any{"gemini-*": "gemini-3.8-flash"}},
	}
	require.Equal(t, "gemini-3.8-flash", mapAntigravityModel(account, "gemini-3.8-flash"))
}

func TestAntigravityRejectsProEvenWithStaleOrWildcardMappings(t *testing.T) {
	for _, mapping := range []map[string]any{nil, {"*": "gemini-3.1-flash-image"}, {"gemini-3-pro-image": "gemini-3.1-flash-image", "nano-pro": "gemini-3.1-flash-image"}} {
		a := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{"model_mapping": mapping}}
		for _, id := range domain.GeminiProImageModelIDs() {
			require.False(t, a.IsModelSupported(id), id)
			require.Empty(t, MapAntigravityModel(a, id), id)
		}
	}
	a := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{"model_mapping": map[string]any{"custom": "gemini-3-pro-image", "nano-2": "gemini-3.1-flash-image"}}}
	require.False(t, a.IsModelSupported("custom"))
	require.Equal(t, "gemini-3.1-flash-image", MapAntigravityModel(a, "nano-2"))
}
