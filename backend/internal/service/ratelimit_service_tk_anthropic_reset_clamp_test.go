//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func settingServiceWithAnthropicMaxCooldown(val string) *SettingService {
	vals := map[string]string{}
	if val != "" {
		vals[SettingKeyAnthropicMaxRateLimitCooldownSeconds] = val
	}
	return NewSettingService(&tkThrottleSettingRepo{vals: vals}, &config.Config{})
}

// Unlike the OpenAI twin this is DEFAULT-ON: unset/blank/malformed/negative all
// fall back to 3600 (the safety default), and only an explicit "0" disables it.
func TestAnthropicMaxRateLimitCooldownSeconds(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, 3600, settingServiceWithAnthropicMaxCooldown("").AnthropicMaxRateLimitCooldownSeconds(ctx), "unset => default ON 3600")
	require.Equal(t, 3600, settingServiceWithAnthropicMaxCooldown("-1").AnthropicMaxRateLimitCooldownSeconds(ctx), "negative => default, never silently disable")
	require.Equal(t, 3600, settingServiceWithAnthropicMaxCooldown("nope").AnthropicMaxRateLimitCooldownSeconds(ctx), "malformed => default, never silently disable")
	require.Equal(t, 0, settingServiceWithAnthropicMaxCooldown("0").AnthropicMaxRateLimitCooldownSeconds(ctx), "explicit 0 => disabled")
	require.Equal(t, 1800, settingServiceWithAnthropicMaxCooldown("1800").AnthropicMaxRateLimitCooldownSeconds(ctx), "explicit positive override")
}

func TestTkClampAnthropicWindowReset(t *testing.T) {
	ctx := context.Background()
	sevenDays := time.Now().Add(7 * 24 * time.Hour)

	t.Run("default-on clamps a far-future reset to now+1h", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithAnthropicMaxCooldown("")}
		got := svc.tkClampAnthropicWindowReset(ctx, 1, sevenDays)
		require.WithinDuration(t, time.Now().Add(time.Hour), got, 2*time.Second, "default-on must clamp a 7d reset to now+1h")
	})

	t.Run("explicit 0 disables, returns reset verbatim", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithAnthropicMaxCooldown("0")}
		got := svc.tkClampAnthropicWindowReset(ctx, 1, sevenDays)
		require.WithinDuration(t, sevenDays, got, time.Second, "explicit 0 must trust the upstream reset verbatim")
	})

	t.Run("custom ceiling honored", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithAnthropicMaxCooldown("1800")}
		got := svc.tkClampAnthropicWindowReset(ctx, 1, sevenDays)
		require.WithinDuration(t, time.Now().Add(30*time.Minute), got, 2*time.Second, "7d reset must be clamped to now+30m")
	})

	t.Run("near reset within ceiling is preserved", func(t *testing.T) {
		svc := &RateLimitService{settingService: settingServiceWithAnthropicMaxCooldown("3600")}
		near := time.Now().Add(5 * time.Minute)
		got := svc.tkClampAnthropicWindowReset(ctx, 1, near)
		require.WithinDuration(t, near, got, time.Second, "a reset within the ceiling must be preserved")
	})

	t.Run("nil settingService is a no-op", func(t *testing.T) {
		svc := &RateLimitService{}
		got := svc.tkClampAnthropicWindowReset(ctx, 1, sevenDays)
		require.WithinDuration(t, sevenDays, got, time.Second)
	})
}

// anthropicClampRepoStub records both the scheduling cooldown (SetRateLimited)
// and the usage gauge (UpdateSessionWindow) so a test can assert the clamp
// touches ONLY the scheduling reset.
type anthropicClampRepoStub struct {
	mockAccountRepoForGemini
	rateLimitReset   time.Time
	rateLimitCalls   int
	sessionWindowEnd time.Time
	sessionCalls     int
}

func (r *anthropicClampRepoStub) SetRateLimited(_ context.Context, _ int64, resetAt time.Time) error {
	r.rateLimitCalls++
	r.rateLimitReset = resetAt
	return nil
}

func (r *anthropicClampRepoStub) UpdateSessionWindow(_ context.Context, _ int64, _ *time.Time, windowEnd *time.Time, _ string) error {
	r.sessionCalls++
	if windowEnd != nil {
		r.sessionWindowEnd = *windowEnd
	}
	return nil
}

// persistAnthropicExhaustedWindowLimit is the path oh-3-a actually hit. Assert the
// default-on clamp shortens the SCHEDULING cooldown but leaves the 5h usage-gauge
// window at the ORIGINAL upstream value.
func TestPersistAnthropicExhaustedWindowLimit_ClampsSchedulingNotGauge(t *testing.T) {
	ctx := context.Background()

	t.Run("7d window: scheduling reset clamped to now+1h", func(t *testing.T) {
		repo := &anthropicClampRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		svc.SetSettingService(settingServiceWithAnthropicMaxCooldown("")) // default-on

		original := time.Now().Add(72 * time.Hour)
		headers := http.Header{}
		headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
		headers.Set("anthropic-ratelimit-unified-7d-reset", strconv.FormatInt(original.Unix(), 10))

		account := &Account{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
		require.True(t, svc.persistAnthropicExhaustedWindowLimit(ctx, account, headers))

		require.Equal(t, 1, repo.rateLimitCalls)
		require.WithinDuration(t, time.Now().Add(time.Hour), repo.rateLimitReset, 2*time.Second, "scheduling cooldown must be clamped to now+1h, not the 3d upstream reset")
		require.True(t, repo.rateLimitReset.Before(original.Add(-time.Hour)), "clamped reset must be well before the upstream 3d reset")
		require.Zero(t, repo.sessionCalls, "7d window does not touch the 5h session gauge")
	})

	t.Run("5h window: gauge keeps original upstream window, scheduling clamped", func(t *testing.T) {
		repo := &anthropicClampRepoStub{}
		svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		svc.SetSettingService(settingServiceWithAnthropicMaxCooldown("")) // default-on

		originalEnd := time.Now().Add(4 * time.Hour) // > 1h ceiling, within 6h max-age
		headers := http.Header{}
		headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
		headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(originalEnd.Unix(), 10))

		account := &Account{ID: 6, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
		require.True(t, svc.persistAnthropicExhaustedWindowLimit(ctx, account, headers))

		require.Equal(t, 1, repo.rateLimitCalls)
		require.WithinDuration(t, time.Now().Add(time.Hour), repo.rateLimitReset, 2*time.Second, "scheduling cooldown clamped to now+1h")
		require.Equal(t, 1, repo.sessionCalls)
		require.WithinDuration(t, originalEnd, repo.sessionWindowEnd, 2*time.Second, "usage gauge window-end must stay at the ORIGINAL upstream 5h reset, not the clamp")
	})
}
