//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestObservedUsageWindowExtraKeys_CoversCrossPlatformGauges(t *testing.T) {
	keys := ObservedUsageWindowExtraKeys()
	require.NotEmpty(t, keys)

	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		require.NotEmpty(t, key)
		_, dup := set[key]
		require.False(t, dup, "duplicate key %q", key)
		set[key] = struct{}{}
	}

	for _, key := range []string{
		"session_window_utilization",
		"passive_usage_7d_utilization",
		"newapi_weekly_utilization",
		NewAPIAccountWindowLockExtraKey,
		"codex_5h_used_percent",
		"codex_7d_used_percent",
		"kiro_usage_percent",
		"kiro_sched_utilization",
		"grok_usage_snapshot",
		"grok_sched_utilization",
		"grok_sched_reset_at",
		"grok_sched_usage_updated_at",
		cnExtraKey(PlatformKimi, cnExtraSuffix5hUsed),
		cnExtraKey(PlatformZhipu, cnExtraSuffixWeeklyReset),
		cnExtraKey(PlatformMiniMax, cnExtraSuffixUsageUpdated),
		cnExtraKey(PlatformOpenCodeGo, cnExtraSuffixMonthlyUsed),
		OllamaCloudUsageSnapshotExtraKey,
	} {
		_, ok := set[key]
		require.True(t, ok, "missing observed-usage key %q", key)
	}

	// Local billing counters must stay on reset-quota, not recover-state.
	for _, key := range []string{"quota_used", "quota_daily_used", "quota_weekly_used", "model_rate_limits"} {
		_, ok := set[key]
		require.False(t, ok, "recover-state must not own %q", key)
	}
}

func TestRateLimitService_RecoverAccountState_ClearsObservedUsageWindows(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDAccount: &Account{
			ID:          88,
			Status:      StatusActive,
			Schedulable: true,
			Extra: map[string]any{
				"newapi_weekly_utilization": 1.0,
				"codex_7d_used_percent":     100.0,
				"kiro_usage_percent":        100.0,
			},
		},
	}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	result, err := svc.RecoverAccountState(context.Background(), 88, AccountRecoveryOptions{
		ClearObservedUsageWindows: true,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.ClearedError)
	require.False(t, result.ClearedRateLimit)
	require.True(t, result.ClearedObservedUsage)
	require.Equal(t, 1, repo.clearObservedUsageCalls)
	require.Equal(t, 0, repo.clearRateLimitCalls)
}

func TestRateLimitService_RecoverAccountAfterSuccessfulTest_DoesNotClearObservedUsage(t *testing.T) {
	now := time.Now()
	repo := &rateLimitClearRepoStub{
		getByIDAccount: &Account{
			ID:            42,
			Status:        StatusActive,
			RateLimitedAt: &now,
			Extra: map[string]any{
				"model_rate_limits": map[string]any{
					"gpt-5": map[string]any{"rate_limit_reset_at": now.Format(time.RFC3339)},
				},
				"codex_7d_used_percent": 12.0,
			},
		},
	}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)

	result, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), 42)
	require.NoError(t, err)
	require.True(t, result.ClearedRateLimit)
	require.False(t, result.ClearedObservedUsage)
	require.Equal(t, 0, repo.clearObservedUsageCalls)
	require.Equal(t, 1, repo.clearRateLimitCalls)
}
