//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAntigravityGatewayService_GetMappedModel_ConvergedSurface(t *testing.T) {
	svc := &AntigravityGatewayService{}
	cases := map[string]string{
		"gemini-3.6-flash":       "gemini-3.6-flash-tiered",
		"gemini-3.7-flash":       "gemini-3.7-flash-medium",
		"gemini-3.8-flash":       "gemini-3.8-flash-medium",
		"gemini-3-flash-preview": "gemini-3.8-flash-medium",
		"gemini-3.5-flash-lite":  "gemini-3.6-flash-tiered",
		"gemini-3.1-flash-image": "gemini-3.1-flash-image",
		"gemini-3-pro-image":     "gemini-3.1-flash-image",
		"claude-sonnet-4-6":      "",
	}
	for requested, expected := range cases {
		require.Equal(t, expected, svc.getMappedModel(&Account{Platform: PlatformAntigravity}, requested), requested)
	}
}

func TestMapAntigravityModel_WildcardTargetEqualsRequest(t *testing.T) {
	account := &Account{
		Platform:    PlatformAntigravity,
		Credentials: map[string]any{"model_mapping": map[string]any{"gemini-*": "gemini-3.8-flash"}},
	}
	require.Equal(t, "gemini-3.8-flash", mapAntigravityModel(account, "gemini-3.8-flash"))
}
