//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func settingServiceWithMaxCooldown(val string) *SettingService {
	vals := map[string]string{}
	if val != "" {
		vals[SettingKeyOpenAIMaxRateLimitCooldownSeconds] = val
	}
	return NewSettingService(&tkThrottleSettingRepo{vals: vals}, &config.Config{})
}

func TestOpenAIMaxRateLimitCooldownSeconds(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, 0, settingServiceWithMaxCooldown("").OpenAIMaxRateLimitCooldownSeconds(ctx), "default disabled")
	require.Equal(t, 0, settingServiceWithMaxCooldown("0").OpenAIMaxRateLimitCooldownSeconds(ctx))
	require.Equal(t, 0, settingServiceWithMaxCooldown("-1").OpenAIMaxRateLimitCooldownSeconds(ctx))
	require.Equal(t, 0, settingServiceWithMaxCooldown("nope").OpenAIMaxRateLimitCooldownSeconds(ctx))
	require.Equal(t, 3600, settingServiceWithMaxCooldown("3600").OpenAIMaxRateLimitCooldownSeconds(ctx))
}

// upstream Wei-Shaw/sub2api#1981: opt-in clamp of long upstream resets.
func TestTkClampOpenAIRateLimitReset(t *testing.T) {
	ctx := context.Background()
	sevenDays := time.Now().Add(7 * 24 * time.Hour)

	t.Run("disabled returns reset verbatim", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("")}
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, sevenDays)
		require.WithinDuration(t, sevenDays, got, time.Second, "default OFF must not change the upstream reset")
	})

	t.Run("enabled clamps a far-future reset to the ceiling", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("3600")}
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, sevenDays)
		require.WithinDuration(t, time.Now().Add(time.Hour), got, 2*time.Second, "7d reset must be clamped to now+1h")
	})

	t.Run("enabled leaves a near reset untouched", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("3600")}
		near := time.Now().Add(5 * time.Minute)
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, near)
		require.WithinDuration(t, near, got, time.Second, "a reset within the ceiling must be preserved")
	})
}
