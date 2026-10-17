//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.True(t, tkIsKiroMixedSupply(mixed))
	require.False(t, tkIsKiroMixedSupply(plain))
	require.True(t, tkShouldClearStickyForKiroModelMismatch(mixed, "claude-fable-5"))
	require.False(t, tkShouldClearStickyForKiroModelMismatch(mixed, "claude-sonnet-4-6"))
	require.False(t, tkShouldClearStickyForKiroModelMismatch(plain, "claude-fable-5"))
	require.Equal(t, tkKiroNativeModelPenalty, tkKiroSelectionPenalty(mixed, "claude-fable-5"))
	require.Equal(t, 0, tkKiroSelectionPenalty(mixed, "claude-sonnet-4-6"))
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
