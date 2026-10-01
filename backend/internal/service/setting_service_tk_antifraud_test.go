//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAntifraudSettings_DefaultsAndRoundTrip(t *testing.T) {
	defaults := map[string]string{}
	tkMergeDefaultAntifraudSettings(defaults)
	require.Equal(t, "3", defaults[SettingKeySignupBonusIPDailyLimit])
	require.Equal(t, "true", defaults[SettingKeyTrialUnpaidMediaBlocked])
	require.Equal(t, "2.00000000", defaults[SettingKeyTrialUnpaidMediaMaxBalance])

	result := &SystemSettings{}
	tkApplyAntifraudParsed(map[string]string{}, result)
	require.Equal(t, 3, result.SignupBonusIPDailyLimit)
	require.True(t, result.TrialUnpaidMediaBlocked)
	require.InDelta(t, 2.0, result.TrialUnpaidMediaMaxBalance, 0.0001)

	tkApplyAntifraudParsed(map[string]string{
		SettingKeySignupBonusIPDailyLimit:    "1",
		SettingKeyTrialUnpaidMediaBlocked:    "false",
		SettingKeyTrialUnpaidMediaMaxBalance: "0.5",
	}, result)
	require.Equal(t, 1, result.SignupBonusIPDailyLimit)
	require.False(t, result.TrialUnpaidMediaBlocked)
	require.InDelta(t, 0.5, result.TrialUnpaidMediaMaxBalance, 0.0001)

	updates := map[string]string{}
	(&SettingService{}).tkAppendAntifraudSettingUpdates(updates, &SystemSettings{
		SignupBonusIPDailyLimit:    7,
		TrialUnpaidMediaBlocked:    true,
		TrialUnpaidMediaMaxBalance: 3.25,
	})
	require.Equal(t, "7", updates[SettingKeySignupBonusIPDailyLimit])
	require.Equal(t, "true", updates[SettingKeyTrialUnpaidMediaBlocked])
	require.Equal(t, "3.25000000", updates[SettingKeyTrialUnpaidMediaMaxBalance])
}
