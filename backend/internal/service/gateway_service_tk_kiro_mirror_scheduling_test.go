//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTkShouldClearStickyForKiroMirrorModelMismatch(t *testing.T) {
	stub := &Account{
		Name:     "kiro-us6",
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

func TestTkKiroMirrorStubModelGateUsesSchedulerMetadata(t *testing.T) {
	svc := &GatewayService{}
	stub := &Account{
		Name:     "kiro-us6",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}
	native := &Account{
		Name:     "cc-us6",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
	}

	require.False(t, svc.isModelSupportedByAccount(stub, "claude-fable-5"))
	require.False(t, svc.isModelSupportedByAccount(stub, "claude-opus-4-1"))
	require.True(t, svc.isModelSupportedByAccount(stub, "claude-sonnet-4-6"))
	require.True(t, tkShouldClearStickyForKiroMirrorModelMismatch(stub, "claude-fable-5"))
	require.True(t, svc.isModelSupportedByAccount(native, "claude-fable-5"))
}

func TestComputeAnthropicKiroMirrorStubPenaltiesPrefersNativeRelay(t *testing.T) {
	stub := &Account{
		Name:     "kiro-us6",
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
		Name:     "kiro-us6",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"mirror_platform": PlatformKiro,
		},
	}
	require.Equal(t, 0, tkKiroMirrorStubSelectionPenalty(stub, "gpt-4o"))
}

func TestTkMixedKiroOAuthModelGateMatchesMirrorStub(t *testing.T) {
	mixed := &Account{
		ID:       303,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"mixed_scheduling": true},
	}
	plain := &Account{
		ID:       304,
		Platform: PlatformKiro,
		Type:     AccountTypeOAuth,
	}
	require.True(t, tkIsKiroMixedOrMirrorSupply(mixed))
	require.False(t, tkIsKiroMixedOrMirrorSupply(plain))
	require.True(t, tkShouldClearStickyForKiroMirrorModelMismatch(mixed, "claude-fable-5"))
	require.False(t, tkShouldClearStickyForKiroMirrorModelMismatch(mixed, "claude-sonnet-4-6"))
	require.False(t, tkShouldClearStickyForKiroMirrorModelMismatch(plain, "claude-fable-5"))
	require.Equal(t, tkKiroMirrorStubNativeModelPenalty, tkKiroMirrorStubSelectionPenalty(mixed, "claude-fable-5"))
	require.Equal(t, 0, tkKiroMirrorStubSelectionPenalty(mixed, "claude-sonnet-4-6"))
}

func TestFilterMixedSchedulingAccountsIncludesKiroWithFlag(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformAnthropic},
		{ID: 2, Platform: PlatformKiro, Extra: map[string]any{"mixed_scheduling": true}},
		{ID: 3, Platform: PlatformKiro},
		{ID: 4, Platform: PlatformAntigravity, Extra: map[string]any{"mixed_scheduling": true}},
	}
	got := filterMixedSchedulingAccounts(accounts, true)
	require.Len(t, got, 3)
	ids := []int64{got[0].ID, got[1].ID, got[2].ID}
	require.ElementsMatch(t, []int64{1, 2, 4}, ids)

	platforms, useMixed, _ := schedulablePlatformsForRequest(PlatformAnthropic, false, "claude-sonnet-4-6")
	require.True(t, useMixed)
	require.Contains(t, platforms, PlatformKiro)
	platformsGemini, _, _ := schedulablePlatformsForRequest(PlatformGemini, false, "gemini-2.5-pro")
	require.NotContains(t, platformsGemini, PlatformKiro)
}
