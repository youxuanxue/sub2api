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
		"gemini-3.7-flash":       "gemini-3.7-flash-medium",
		"gemini-3.8-flash":       "gemini-3.8-flash-medium",
		"gemini-3-flash":         "gemini-3.8-flash-medium",
		"gemini-3-flash-preview": "gemini-3.8-flash-medium",
		"gemini-3.5-flash-lite":  "gemini-3.6-flash-tiered",
		"gemini-3.1-flash-image": "gemini-3.1-flash-image",
		"gemini-3-pro-image":     "gemini-3.1-flash-image",
		"nano-2":                 "gemini-3.1-flash-image",
		"nano-pro":               "gemini-3.1-flash-image",
		"claude-sonnet-4-6":      "",
	}
	for requested, expected := range cases {
		require.Equal(t, expected, svc.getMappedModel(&Account{Platform: PlatformAntigravity}, requested), requested)
	}
}

func TestAntigravityDefaultModels_SubsetOfDefaultMapping(t *testing.T) {
	t.Parallel()
	// Public listing IDs must resolve through the default remap owner. nano-pro is
	// remap-only (catalog excludes it) and intentionally absent from DefaultModels.
	hasNanoProListing := false
	for _, m := range antigravity.DefaultModels() {
		if m.ID == "nano-pro" {
			hasNanoProListing = true
		}
		_, ok := domain.DefaultAntigravityModelMapping[m.ID]
		require.True(t, ok, "DefaultModels id %q missing from DefaultAntigravityModelMapping", m.ID)
	}
	require.False(t, hasNanoProListing, "nano-pro must stay remap-only, not listed in DefaultModels")
	require.Equal(t, "gemini-3.1-flash-image", domain.DefaultAntigravityModelMapping["nano-pro"])
}

func TestMapAntigravityModel_WildcardTargetEqualsRequest(t *testing.T) {
	account := &Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"model_mapping": map[string]any{"gemini-*": "gemini-3.8-flash"}},
	}
	require.Equal(t, "gemini-3.8-flash", mapAntigravityModel(account, "gemini-3.8-flash"))
}
