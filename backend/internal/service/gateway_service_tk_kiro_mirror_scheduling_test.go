//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkShouldClearStickyForKiroMirrorModelMismatch(t *testing.T) {
	stub := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
			"base_url":        "https://api-us6.tokenkey.dev",
		},
	}
	native := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api-us5.tokenkey.dev",
		},
	}

	require.True(t, tkShouldClearStickyForKiroMirrorModelMismatch(stub, "claude-fable-5"))
	require.False(t, tkShouldClearStickyForKiroMirrorModelMismatch(stub, "claude-sonnet-4-6"))
	require.False(t, tkShouldClearStickyForKiroMirrorModelMismatch(native, "claude-fable-5"))
	require.True(t, shouldClearStickySession(stub, "claude-opus-4-1"))
}

func TestComputeAnthropicKiroMirrorStubPenaltiesPrefersNativeRelay(t *testing.T) {
	stub := &Account{
		ID:       10,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Priority: 0,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	native := &Account{
		ID:       11,
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Priority: 5,
		Credentials: map[string]any{
			"base_url": "https://api-us5.tokenkey.dev",
		},
	}
	candidates := []accountWithLoad{
		{account: stub},
		{account: native},
	}
	computeAnthropicKiroMirrorStubPenalties(candidates, "claude-fable-5")
	require.Equal(t, tkKiroMirrorStubNativeModelPenalty, candidates[0].saturationPenalty)
	require.Equal(t, 0, candidates[1].saturationPenalty)

	selected := filterByMinPriority(candidates)
	require.Len(t, selected, 1)
	require.Equal(t, int64(11), selected[0].account.ID)
}

func TestTkKiroMirrorStubSelectionPenaltyIgnoresNonAnthropicModels(t *testing.T) {
	stub := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	require.Equal(t, 0, tkKiroMirrorStubSelectionPenalty(stub, "gpt-4o"))
}
