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

// DEFAULT-ON (flipped for parity with the Anthropic clamp): unset/blank/malformed/
// negative fall back to 3600; only an explicit "0" disables it.
func TestOpenAIMaxRateLimitCooldownSeconds(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, 3600, settingServiceWithMaxCooldown("").OpenAIMaxRateLimitCooldownSeconds(ctx), "unset => default ON 3600")
	require.Equal(t, 3600, settingServiceWithMaxCooldown("-1").OpenAIMaxRateLimitCooldownSeconds(ctx), "negative => default, never silently disable")
	require.Equal(t, 3600, settingServiceWithMaxCooldown("nope").OpenAIMaxRateLimitCooldownSeconds(ctx), "malformed => default, never silently disable")
	require.Equal(t, 0, settingServiceWithMaxCooldown("0").OpenAIMaxRateLimitCooldownSeconds(ctx), "explicit 0 => disabled")
	require.Equal(t, 1800, settingServiceWithMaxCooldown("1800").OpenAIMaxRateLimitCooldownSeconds(ctx), "explicit positive override")
}

// upstream Wei-Shaw/sub2api#1981: opt-in clamp of long upstream resets.
func TestTkClampOpenAIRateLimitReset(t *testing.T) {
	ctx := context.Background()
	sevenDays := time.Now().Add(7 * 24 * time.Hour)

	t.Run("explicit 0 disables, returns reset verbatim", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("0")}
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, sevenDays)
		require.WithinDuration(t, sevenDays, got, time.Second, "explicit 0 must trust the upstream reset verbatim")
	})

	t.Run("default-on clamps a far-future reset to now+1h", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("")}
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, sevenDays)
		require.WithinDuration(t, time.Now().Add(time.Hour), got, 2*time.Second, "default-on must clamp a 7d reset to now+1h")
	})

	t.Run("enabled leaves a near reset untouched", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithMaxCooldown("3600")}
		near := time.Now().Add(5 * time.Minute)
		got := svc.tkClampOpenAIRateLimitReset(ctx, 1, near)
		require.WithinDuration(t, near, got, time.Second, "a reset within the ceiling must be preserved")
	})
}
