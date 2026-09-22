package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountWideModelRateLimitCascade_Threshold(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	future := func(d time.Duration) string {
		return now.Add(d).Format(time.RFC3339)
	}
	mkLimits := func(models ...string) map[string]any {
		limits := make(map[string]any, len(models))
		for i, model := range models {
			limits[model] = map[string]any{
				"rate_limited_at":     now.Add(-time.Hour).Format(time.RFC3339),
				"rate_limit_reset_at": future(time.Duration(i+1) * time.Hour),
			}
		}
		return limits
	}

	t.Run("three active models stay schedulable", func(t *testing.T) {
		account := &Account{
			ID: 1, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{modelRateLimitsKey: mkLimits("m1", "m2", "m3")},
		}
		require.False(t, account.IsRateLimited())
		require.True(t, account.IsSchedulable())
		require.Nil(t, AccountWideRateLimitResetAt(account, now))
	})

	t.Run("four active models cascade to account-wide block", func(t *testing.T) {
		account := &Account{
			ID: 129, Name: "ali-token-plan", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{modelRateLimitsKey: mkLimits(
				"deepseek-v4-flash", "deepseek-v4-pro", "qwen3.7-max", "glm-5.3",
			)},
		}
		require.True(t, account.IsRateLimited())
		require.False(t, account.IsSchedulable())
		reset := AccountWideRateLimitResetAt(account, now)
		require.NotNil(t, reset)
		// Four scopes expire at +1h..+4h; need one to clear → unlock at earliest (+1h).
		require.WithinDuration(t, now.Add(time.Hour), *reset, time.Second)
	})

	t.Run("AICredits does not count toward threshold", func(t *testing.T) {
		limits := mkLimits("m1", "m2", "m3")
		limits[creditsExhaustedKey] = map[string]any{
			"rate_limited_at":     now.Add(-time.Hour).Format(time.RFC3339),
			"rate_limit_reset_at": future(5 * time.Hour),
		}
		account := &Account{
			ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{modelRateLimitsKey: limits},
		}
		require.False(t, account.IsRateLimited())
		require.True(t, account.IsSchedulable())
	})

	t.Run("applies to anthropic oauth the same way", func(t *testing.T) {
		account := &Account{
			ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{modelRateLimitsKey: mkLimits(
				"anthropic:class:opus", "anthropic:class:sonnet",
				"claude-fable-5", "claude-opus-4-6",
			)},
		}
		require.True(t, account.IsRateLimited())
		require.False(t, account.IsSchedulable())
	})

	t.Run("five models unlock after second earliest expiry", func(t *testing.T) {
		account := &Account{
			ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true,
			Extra: map[string]any{modelRateLimitsKey: mkLimits("a", "b", "c", "d", "e")},
		}
		reset := AccountWideRateLimitResetAt(account, now)
		require.NotNil(t, reset)
		require.WithinDuration(t, now.Add(2*time.Hour), *reset, time.Second)
	})

	t.Run("newapi false window lock still ignored when under threshold", func(t *testing.T) {
		resetAt := now.Add(3 * time.Hour)
		account := &Account{
			ID: 5, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true,
			RateLimitResetAt: &resetAt,
			RateLimitedAt:    &resetAt,
			Extra: map[string]any{
				newAPIWeeklyUtilExtraKey:  1.0,
				newAPIWeeklyResetExtraKey: float64(resetAt.Unix()),
				modelRateLimitsKey:        mkLimits("only-one"),
			},
		}
		require.True(t, NewAPIAccountWindowLockIgnored(account))
		require.False(t, account.IsRateLimited())
		require.True(t, account.IsSchedulable())
	})

	t.Run("newapi false window lock plus overflow still blocks", func(t *testing.T) {
		resetAt := now.Add(3 * time.Hour)
		account := &Account{
			ID: 6, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true,
			RateLimitResetAt: &resetAt,
			RateLimitedAt:    &resetAt,
			Extra: map[string]any{
				newAPIWeeklyUtilExtraKey:  1.0,
				newAPIWeeklyResetExtraKey: float64(resetAt.Unix()),
				modelRateLimitsKey: mkLimits(
					"deepseek-v4-flash", "qwen3.7-max", "qwen3.8-max", "glm-5.3",
				),
			},
		}
		require.True(t, NewAPIAccountWindowLockIgnored(account))
		require.True(t, account.IsRateLimited(), "model cascade must still mark the account rate-limited")
		require.False(t, account.IsSchedulable())
		projected := AccountWideRateLimitResetAt(account, now)
		require.NotNil(t, projected)
		require.WithinDuration(t, now.Add(time.Hour), *projected, time.Second)
	})
}
