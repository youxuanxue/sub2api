//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func anthropicHotAccount(util float64, now time.Time) *Account {
	windowEnd := now.Add(4 * time.Hour)
	windowStart := now.Add(-1 * time.Hour)
	return &Account{
		Platform:           PlatformAnthropic,
		Type:               AccountTypeOAuth,
		SessionWindowStart: &windowStart,
		SessionWindowEnd:   &windowEnd,
		Extra: map[string]any{
			"session_window_utilization": util,
			"passive_usage_sampled_at":   now.Format(time.RFC3339),
		},
	}
}

func TestAnthropicAccountWindowUtilization(t *testing.T) {
	now := time.Now()
	windowEnd := now.Add(4 * time.Hour)
	windowStart := now.Add(-1 * time.Hour)

	t.Run("no extra => no signal", func(t *testing.T) {
		_, ok := anthropicAccountWindowUtilization(&Account{}, now)
		require.False(t, ok)
	})
	t.Run("takes the worse of 5h/7d", func(t *testing.T) {
		acc := &Account{
			SessionWindowStart: &windowStart,
			SessionWindowEnd:   &windowEnd,
			Extra: map[string]any{
				"session_window_utilization":   0.60,
				"passive_usage_7d_utilization": 0.90,
				"passive_usage_sampled_at":     now.Format(time.RFC3339),
			},
		}
		util, ok := anthropicAccountWindowUtilization(acc, now)
		require.True(t, ok)
		require.InDelta(t, 0.90, util, 1e-9)
	})
	t.Run("stale snapshot => no signal", func(t *testing.T) {
		acc := anthropicHotAccount(0.99, now)
		acc.Extra["passive_usage_sampled_at"] = now.Add(-3 * time.Hour).Format(time.RFC3339)
		_, ok := anthropicAccountWindowUtilization(acc, now)
		require.False(t, ok)
	})
	t.Run("expired 5h window => no 5h signal but 7d may remain", func(t *testing.T) {
		pastEnd := now.Add(-1 * time.Minute)
		acc := &Account{
			SessionWindowEnd: &pastEnd,
			Extra: map[string]any{
				"session_window_utilization":   0.99,
				"passive_usage_7d_utilization": 0.40,
				"passive_usage_sampled_at":     now.Format(time.RFC3339),
			},
		}
		util, ok := anthropicAccountWindowUtilization(acc, now)
		require.True(t, ok)
		require.InDelta(t, 0.40, util, 1e-9)
	})
}

func TestIsAccountSchedulableForAnthropicWindow(t *testing.T) {
	svc := &GatewayService{}
	ctx := context.Background()
	now := time.Now()

	t.Run("nil account => schedulable", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, nil, false))
	})
	t.Run("non-oauth anthropic => schedulable", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, false))
	})
	t.Run("cool account => schedulable both ways", func(t *testing.T) {
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(0.50, now), false))
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(0.50, now), true))
	})
	t.Run("sticky-only band: kept for sticky, dropped for load-balance", func(t *testing.T) {
		require.False(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(0.98, now), false))
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(0.98, now), true))
	})
	t.Run("not-schedulable band: dropped even for sticky", func(t *testing.T) {
		require.False(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(1.0, now), false))
		require.False(t, svc.isAccountSchedulableForAnthropicWindow(ctx, anthropicHotAccount(1.0, now), true))
	})
	t.Run("per-account disable => schedulable", func(t *testing.T) {
		acc := anthropicHotAccount(0.995, now)
		acc.Extra["anthropic_window_guard_disabled"] = true
		require.True(t, svc.isAccountSchedulableForAnthropicWindow(ctx, acc, false))
	})
}

func TestLeastUtilizedAnthropicAccount(t *testing.T) {
	now := time.Now()
	a := anthropicHotAccount(0.99, now)
	a.ID = 1
	b := anthropicHotAccount(0.98, now)
	b.ID = 2
	c := anthropicHotAccount(0.995, now)
	c.ID = 3
	best := leastUtilizedAnthropicAccount([]*Account{a, b, c}, now)
	require.NotNil(t, best)
	require.Equal(t, int64(2), best.ID)

	require.Nil(t, leastUtilizedAnthropicAccount(nil, now))
}
