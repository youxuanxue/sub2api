//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const miniMaxTokenPlanQuotaBody = `{"error":{"message":"已达到 Token Plan 用量上限：请升级 Token Plan 套餐或购买积分补充用量。 (2056)","type":"insufficient_quota"}}`

func TestTkIsMiniMaxTokenPlanQuotaMessage(t *testing.T) {
	require.True(t, tkIsMiniMaxTokenPlanQuotaMessage(miniMaxTokenPlanQuotaBody))
	require.True(t, tkIsMiniMaxTokenPlanQuotaMessage("已达到 Token Plan 用量上限 (2056)"))
	require.False(t, tkIsMiniMaxTokenPlanQuotaMessage(qianfanMonthlyQuotaBody))
	require.False(t, tkIsMiniMaxTokenPlanQuotaMessage("Token Plan Person monthly quota limit exceeded"))
	require.False(t, tkIsMiniMaxTokenPlanQuotaMessage("rate limit exceeded"))
	require.True(t, tkIsRecoverableUsageWindowMessage(miniMaxTokenPlanQuotaBody))
	require.False(t, tkIsAccountStandingBillingFailure(miniMaxTokenPlanQuotaBody, nil))
}

func TestTkParseNewAPIUsageWindowResponse_MiniMaxTokenPlanUses5hFallback(t *testing.T) {
	now := time.Date(2026, 11, 20, 2, 46, 0, 0, time.UTC)
	for _, retry := range []string{"", "5", "60"} {
		hit := tkParseNewAPIUsageWindowResponse(miniMaxTokenPlanQuotaBody, http.Header{"Retry-After": {retry}}, now)
		require.NotNil(t, hit, "MiniMax Token Plan must not fall through to the short 429 fallback")
		require.Equal(t, "5h", hit.Window)
		require.Equal(t, now.Add(5*time.Hour), hit.ResetAt.UTC())
	}
}

func TestHandleUpstreamError_MiniMaxTokenPlanQuotaCools5hNotFallback(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 592, Platform: PlatformNewAPI, ChannelType: 1, Type: AccountTypeAPIKey, Schedulable: true}

	before := time.Now()
	_ = svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		http.Header{"Retry-After": {"5"}},
		[]byte(miniMaxTokenPlanQuotaBody),
	)
	after := time.Now()

	// 429 scheduling cool is rate_limit_reset_at, not shouldDisable (Qianfan SSOT).
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.tempCalls)
	require.WithinDuration(t, before.Add(5*time.Hour), repo.lastRateLimitedResetAt, after.Sub(before)+time.Second)
	require.Equal(t, 1.0, repo.lastExtraUpdates[newAPIFiveHourUtilExtraKey])
}

func TestIsNewAPIMiniMaxTokenPlanAccount(t *testing.T) {
	t.Parallel()
	require.True(t, isNewAPIMiniMaxTokenPlanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimaxi.com"},
	}))
	require.True(t, isNewAPIMiniMaxTokenPlanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimax.io/v1"},
	}))
	require.False(t, isNewAPIMiniMaxTokenPlanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.moonshot.cn/v1"},
	}))
	require.False(t, isNewAPIMiniMaxTokenPlanAccount(&Account{
		Platform:    PlatformMiniMax,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimaxi.com", "account_mode": AccountModeCoding},
	}))
	require.Equal(t, PlatformMiniMax, (&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimaxi.com"},
	}).GetCodingPlanProvider())
	require.NoError(t, validateCodingPlanAccount(&Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimaxi.com", "api_key": "k"},
	}))
}

func TestGetCNAPIKey_NewAPIMiniMaxTokenPlan(t *testing.T) {
	t.Parallel()
	// Positive: NewAPI+minimaxi shares remains with native minimax; key must be readable.
	mm := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.minimaxi.com",
			"api_key":  "sk-mm-test",
		},
	}
	require.Equal(t, PlatformMiniMax, mm.GetCodingPlanProvider())
	require.Equal(t, "sk-mm-test", mm.GetCNAPIKey())

	ioHost := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.minimax.io/v1",
			"api_key":  "sk-mm-io",
		},
	}
	require.Equal(t, "sk-mm-io", ioHost.GetCNAPIKey())

	// Negative: unrelated NewAPI host stays outside GetCNAPIKey (not multi-protocol).
	other := &Account{
		Platform: PlatformNewAPI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.moonshot.cn/v1",
			"api_key":  "sk-other",
		},
	}
	require.Empty(t, other.GetCodingPlanProvider())
	require.Empty(t, other.GetCNAPIKey())

	// Negative: NewAPI MiniMax without api_key still surfaces empty (true missing cred).
	missing := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://api.minimaxi.com"},
	}
	require.Equal(t, PlatformMiniMax, missing.GetCodingPlanProvider())
	require.Empty(t, missing.GetCNAPIKey())
}

func TestMirrorMiniMaxRemainsToNewAPIUsageWindowExtra(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)
	reset5h := now.Add(2 * time.Hour).Format(time.RFC3339)
	resetWeekly := now.Add(48 * time.Hour).Format(time.RFC3339)
	got := mirrorMiniMaxRemainsToNewAPIUsageWindowExtra([]CNQuotaTier{
		{Window: "5h", UsedPercent: 21.5, ResetAt: reset5h},
		{Window: "weekly", UsedPercent: 79.2, ResetAt: resetWeekly},
	}, now)
	require.Equal(t, 1.0, got[newAPIUsageWindowObservedExtraKey])
	require.InDelta(t, 0.215, got[newAPIFiveHourUtilExtraKey], 1e-9)
	require.InDelta(t, 0.792, got[newAPIWeeklyUtilExtraKey], 1e-9)
	require.Equal(t, float64(now.Add(2*time.Hour).Unix()), got[newAPIFiveHourResetExtraKey])
	require.Nil(t, got[NewAPIAccountWindowLockExtraKey], "partial 5h must clear stale window_lock")

	exhausted := mirrorMiniMaxRemainsToNewAPIUsageWindowExtra([]CNQuotaTier{
		{Window: "5h", UsedPercent: 100, ResetAt: reset5h},
	}, now)
	require.Equal(t, 1.0, exhausted[NewAPIAccountWindowLockExtraKey])
	require.Equal(t, 1.0, exhausted[newAPIFiveHourUtilExtraKey])
}

func TestApplyNewAPIUsageWindowSnapshot_ObservedPartialWithoutLock(t *testing.T) {
	t.Parallel()
	resetAt := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	account := &Account{
		ID:       241,
		Platform: PlatformNewAPI,
		Extra: map[string]any{
			newAPIUsageWindowObservedExtraKey: 1.0,
			newAPIFiveHourUtilExtraKey:        0.215,
			newAPIFiveHourResetExtraKey:       float64(resetAt.Unix()),
			newAPIWeeklyUtilExtraKey:          0.792,
			newAPIWeeklyResetExtraKey:         float64(resetAt.Add(24 * time.Hour).Unix()),
		},
	}
	usage := &UsageInfo{Source: "passive"}
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.NotNil(t, usage.FiveHour)
	require.InDelta(t, 21.5, usage.FiveHour.Utilization, 1e-6)
	require.NotNil(t, usage.SevenDay)
	require.InDelta(t, 79.2, usage.SevenDay.Utilization, 1e-6)

	// Without observed/lock, partial Extra must stay hidden (Volc false-lock SSOT).
	hidden := &Account{
		ID:       242,
		Platform: PlatformNewAPI,
		Extra: map[string]any{
			newAPIFiveHourUtilExtraKey:  0.5,
			newAPIFiveHourResetExtraKey: float64(resetAt.Unix()),
		},
	}
	usage2 := &UsageInfo{Source: "passive"}
	applyNewAPIUsageWindowSnapshot(hidden, usage2)
	require.Nil(t, usage2.FiveHour)
	require.Nil(t, usage2.UpstreamQuota)
}

func TestHandleUpstreamError_MiniMaxTokenPlanPrefersExtraSnapshot(t *testing.T) {
	reset := time.Now().Add(37 * time.Minute).UTC().Truncate(time.Second)
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{
		ID:          591,
		Platform:    PlatformNewAPI,
		ChannelType: 1,
		Type:        AccountTypeAPIKey,
		Schedulable: true,
		Extra: map[string]any{
			newAPIFiveHourResetExtraKey: float64(reset.Unix()),
			newAPIFiveHourUtilExtraKey:  1.0,
		},
	}

	_ = svc.HandleUpstreamError(
		context.Background(),
		account,
		http.StatusTooManyRequests,
		nil,
		[]byte(miniMaxTokenPlanQuotaBody),
	)

	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.True(t, reset.Equal(repo.lastRateLimitedResetAt.UTC()), "want Extra remains end_time, got %s", repo.lastRateLimitedResetAt)
}
