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

// schedulingThresholdObservedExtraKeys lists Extra keys that can drive
// EvaluateAccountSchedulingThreshold / openAICodexSnapshotStaleForPause.
// Keep this list in sync with account_scheduling_threshold_eval.go readers —
// the subset assertion below is the mechanical guard against silent SSOT drift.
func schedulingThresholdObservedExtraKeys() []string {
	keys := []string{
		"session_window_utilization",
		"passive_usage_7d_utilization",
		"passive_usage_7d_reset",
		"passive_usage_7d_oi_utilization",
		"passive_usage_7d_oi_reset",
		"codex_5h_used_percent",
		"codex_5h_reset_at",
		"codex_5h_reset_after_seconds",
		"codex_7d_used_percent",
		"codex_7d_reset_at",
		"codex_7d_reset_after_seconds",
		"codex_usage_updated_at",
		"grok_sched_utilization",
		"grok_sched_reset_at",
	}
	return append(keys, cnObservedUsageWindowExtraKeys()...)
}

func TestObservedUsageWindowExtraKeys_CoversSchedulingThresholdInputs(t *testing.T) {
	set := make(map[string]struct{})
	for _, key := range ObservedUsageWindowExtraKeys() {
		set[key] = struct{}{}
	}
	for _, key := range schedulingThresholdObservedExtraKeys() {
		_, ok := set[key]
		require.True(t, ok, "threshold gauge %q must be cleared by ObservedUsageWindowExtraKeys", key)
	}
}

func TestClearingObservedUsageWindows_PreventsSchedulingThresholdRepause(t *testing.T) {
	now := time.Now().UTC()
	until := now.Add(6 * time.Hour).Format(time.RFC3339)
	thresholds := map[string]int{
		PlatformOpenAI: 80,
		PlatformGrok:   80,
		PlatformKimi:   80,
	}

	cases := []struct {
		name     string
		platform string
		extra    map[string]any
	}{
		{
			name:     "openai_codex_7d",
			platform: PlatformOpenAI,
			extra: map[string]any{
				"codex_7d_used_percent":  95.0,
				"codex_7d_reset_at":      until,
				"codex_usage_updated_at": now.Format(time.RFC3339),
			},
		},
		{
			name:     "grok_sched",
			platform: PlatformGrok,
			extra: map[string]any{
				"grok_sched_utilization": 95.0,
				"grok_sched_reset_at":    until,
			},
		},
		{
			name:     "kimi_coding_plan",
			platform: PlatformKimi,
			extra: map[string]any{
				cnExtraKey(PlatformKimi, cnExtraSuffix5hUsed):  95.0,
				cnExtraKey(PlatformKimi, cnExtraSuffix5hReset): until,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID:          42,
				Platform:    tc.platform,
				Status:      StatusActive,
				Schedulable: true,
				Extra:       cloneStringAnyMap(tc.extra),
			}
			before := EvaluateAccountSchedulingThreshold(account, thresholds, now)
			require.True(t, before.ShouldPause, "stale gauges must pause before clear")

			for _, key := range ObservedUsageWindowExtraKeys() {
				delete(account.Extra, key)
			}
			after := EvaluateAccountSchedulingThreshold(account, thresholds, now)
			require.False(t, after.ShouldPause, "recover SSOT clear must wait for fresh evidence")
		})
	}
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func TestRateLimitService_RecoverAccountState_ClearsObservedUsageWindows(t *testing.T) {
	repo := &rateLimitClearRepoStub{
		getByIDAccount: &Account{
			ID:          88,
			Status:      StatusActive,
			Schedulable: true,
			Extra: map[string]any{
				"newapi_weekly_utilization":                   1.0,
				"codex_7d_used_percent":                       100.0,
				"kiro_usage_percent":                          100.0,
				"grok_sched_utilization":                      95.0,
				cnExtraKey(PlatformKimi, cnExtraSuffix5hUsed): 90.0,
				"keep_me": "ok",
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
	require.NotContains(t, repo.getByIDAccount.Extra, "codex_7d_used_percent")
	require.NotContains(t, repo.getByIDAccount.Extra, "grok_sched_utilization")
	require.NotContains(t, repo.getByIDAccount.Extra, cnExtraKey(PlatformKimi, cnExtraSuffix5hUsed))
	require.Equal(t, "ok", repo.getByIDAccount.Extra["keep_me"])
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
	require.Equal(t, 12.0, repo.getByIDAccount.Extra["codex_7d_used_percent"])
}
