//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type tkThrottleSettingRepo struct {
	SettingRepository
	vals map[string]string
}

func (r *tkThrottleSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = r.vals[k]
	}
	return out, nil
}

type tkThrottleAccountRepo struct {
	AccountRepository
	tempCalls  int
	lastUntil  time.Time
	lastReason string
}

func (r *tkThrottleAccountRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.tempCalls++
	r.lastUntil = until
	r.lastReason = reason
	return nil
}

func settingServiceWithThrottle(val string) *SettingService {
	vals := map[string]string{}
	if val != "" {
		vals[SettingKeyOpenAIImplicitThrottleCooldownSeconds] = val
	}
	return NewSettingService(&tkThrottleSettingRepo{vals: vals}, &config.Config{})
}

func TestOpenAIImplicitThrottleCooldownSeconds(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, 0, settingServiceWithThrottle("").OpenAIImplicitThrottleCooldownSeconds(ctx), "default disabled")
	require.Equal(t, 0, settingServiceWithThrottle("0").OpenAIImplicitThrottleCooldownSeconds(ctx))
	require.Equal(t, 0, settingServiceWithThrottle("-5").OpenAIImplicitThrottleCooldownSeconds(ctx), "negative clamps to disabled")
	require.Equal(t, 0, settingServiceWithThrottle("abc").OpenAIImplicitThrottleCooldownSeconds(ctx), "non-numeric clamps to disabled")
	require.Equal(t, 45, settingServiceWithThrottle("45").OpenAIImplicitThrottleCooldownSeconds(ctx))
}

// upstream Wei-Shaw/sub2api#2727: opt-in cross-request cooldown.
func TestTkApplyImplicitThrottleCooldown(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 2727}

	t.Run("disabled by default does nothing", func(t *testing.T) {
		repo := &tkThrottleAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo, settingService: settingServiceWithThrottle("")}
		svc.tkApplyImplicitThrottleCooldown(ctx, account, 502)
		require.Equal(t, 0, repo.tempCalls, "default OFF must not change production behavior")
	})

	t.Run("enabled benches account on 5xx", func(t *testing.T) {
		repo := &tkThrottleAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo, settingService: settingServiceWithThrottle("30")}
		svc.tkApplyImplicitThrottleCooldown(ctx, account, 502)
		require.Equal(t, 1, repo.tempCalls)
		require.InDelta(t, 30*time.Second, time.Until(repo.lastUntil), float64(2*time.Second))
		require.Contains(t, repo.lastReason, "implicit-throttle")
	})

	t.Run("enabled ignores non-5xx", func(t *testing.T) {
		repo := &tkThrottleAccountRepo{}
		svc := &OpenAIGatewayService{accountRepo: repo, settingService: settingServiceWithThrottle("30")}
		svc.tkApplyImplicitThrottleCooldown(ctx, account, 400)
		svc.tkApplyImplicitThrottleCooldown(ctx, account, 429)
		svc.tkApplyImplicitThrottleCooldown(ctx, account, 529) // overload: handle529 owns the cooldown
		require.Equal(t, 0, repo.tempCalls, "auth/ratelimit/overload statuses have dedicated handling")
	})
}
