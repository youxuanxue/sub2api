//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKiroMirrorStubSupportsModel_DatedHaikuSnapshotID(t *testing.T) {
	// prod P0 2026-07-06: user16 key222 hammered claude-haiku-4-5-20251001 on group1;
	// all four kiro-us* mirror stubs were filtered as model_unsupported because
	// kiroMirrorStubSupportsModel only matched short catalog IDs.
	stub := &Account{
		Name:     "kiro-us6",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
			"base_url":        "https://api-us6.tokenkey.dev",
		},
	}
	require.True(t, stub.IsKiroMirrorStub())
	require.True(t, stub.IsModelSupported("claude-haiku-4-5-20251001"))
	require.True(t, stub.IsModelSupported("claude-haiku-4-5"))
	require.False(t, stub.IsModelSupported("claude-fable-5"))
}

func TestGatewayService_KiroMirrorStubSupportsDatedHaiku(t *testing.T) {
	svc := &GatewayService{}
	stub := &Account{
		Name:     "kiro-us5",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	require.True(t, svc.isModelSupportedByAccount(stub, "claude-haiku-4-5-20251001"))
}
