//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTkParseNewAPIUsageWindowHit_WeeklyWithReset(t *testing.T) {
	hit := tkParseNewAPIUsageWindowHit(
		"You have exceeded the weekly usage quota. It will reset at 2026-09-07 00:00:00 +0800 CST",
	)
	require.NotNil(t, hit)
	require.Equal(t, "weekly", hit.Window)
	require.Equal(t, 2026, hit.ResetAt.Year())
	require.Equal(t, time.September, hit.ResetAt.Month())
	require.Equal(t, 7, hit.ResetAt.Day())
	_, offset := hit.ResetAt.Zone()
	require.Equal(t, 8*3600, offset)
}

func TestTkParseNewAPIUsageWindowHit_RejectsBurstAndStanding(t *testing.T) {
	require.Nil(t, tkParseNewAPIUsageWindowHit(
		"System protection triggered by request burst. Please slow down traffic growth",
	))
	require.Nil(t, tkParseNewAPIUsageWindowHit(
		"Insufficient Balance. Please recharge your account",
	))
	require.Nil(t, tkParseNewAPIUsageWindowHit(
		"You have exceeded the weekly usage quota.", // no reset timestamp
	))
}

func TestTkTryHandleNewAPIUsageWindow429_PersistsExtraAndCoolsUntilReset(t *testing.T) {
	resetAt := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	msg := "You have exceeded the weekly usage quota. It will reset at " +
		resetAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST")
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	require.NoError(t, err)

	repo := &rateLimitAccountRepoStub{
		accountOnGet: &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Extra: map[string]any{}},
	}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Extra: map[string]any{}}

	require.True(t, svc.tkTryHandleNewAPIUsageWindow429(context.Background(), account, body))
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.WithinDuration(t, resetAt.UTC(), repo.lastRateLimitedResetAt.UTC(), time.Second)
	require.NotNil(t, repo.lastExtraUpdates)
	require.Equal(t, 1.0, repo.lastExtraUpdates[newAPIWeeklyUtilExtraKey])
	require.Equal(t, float64(resetAt.Unix()), repo.lastExtraUpdates[newAPIWeeklyResetExtraKey])
	require.Equal(t, 1.0, account.Extra[newAPIWeeklyUtilExtraKey])
	require.Equal(t, float64(resetAt.Unix()), account.Extra[newAPIWeeklyResetExtraKey])
}

func TestApplyNewAPIUsageWindowSnapshot_SurfacesWeeklyOnSevenDay(t *testing.T) {
	resetAt := time.Now().Add(24 * time.Hour).UTC()
	account := &Account{
		ID:       88,
		Platform: PlatformNewAPI,
		Extra: map[string]any{
			newAPIWeeklyUtilExtraKey:  1.0,
			newAPIWeeklyResetExtraKey: float64(resetAt.Unix()),
		},
	}
	usage := &UsageInfo{
		Source: "passive",
		SevenDay: &UsageProgress{
			Utilization: 0,
			WindowStats: &WindowStats{Requests: 15100, Cost: 269.96},
		},
	}
	applyNewAPIUsageWindowSnapshot(account, usage)

	require.Equal(t, 100.0, usage.SevenDay.Utilization)
	require.NotNil(t, usage.SevenDay.ResetsAt)
	require.WithinDuration(t, resetAt, *usage.SevenDay.ResetsAt, time.Second)
	require.Equal(t, int64(15100), usage.SevenDay.WindowStats.Requests)
	require.NotNil(t, usage.UpstreamQuota)
	require.Equal(t, "degraded", usage.UpstreamQuota.State)
	require.Equal(t, "rate_limited", usage.UpstreamQuota.ErrorCode)
	require.NotEmpty(t, usage.UpstreamQuota.Dimensions)
	require.Equal(t, newAPIUpstreamWeeklyKey, usage.UpstreamQuota.Dimensions[0].Key)
}

func TestApplyNewAPIUsageWindowSnapshot_IgnoresExpiredReset(t *testing.T) {
	account := &Account{
		ID:       88,
		Platform: PlatformNewAPI,
		Extra: map[string]any{
			newAPIWeeklyUtilExtraKey:  1.0,
			newAPIWeeklyResetExtraKey: float64(time.Now().Add(-time.Hour).Unix()),
		},
	}
	usage := &UsageInfo{SevenDay: &UsageProgress{Utilization: 0}}
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.Equal(t, 0.0, usage.SevenDay.Utilization)
	require.Nil(t, usage.UpstreamQuota)
}

func TestBuildNewAPIUpstreamQuota_UnknownWithoutSnapshot(t *testing.T) {
	account := &Account{ID: 88, Platform: PlatformNewAPI, Extra: map[string]any{}}
	usage := &UsageInfo{Source: "passive"}
	got := buildNewAPIUpstreamQuota(account, usage)
	require.Equal(t, "unknown", got.State)
	require.Empty(t, got.Dimensions)
	require.Empty(t, got.ErrorCode)
}

func TestHandle429_NewAPIWeeklyUsesResetNotFallback(t *testing.T) {
	resetAt := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	msg := "You have exceeded the weekly usage quota. It will reset at " +
		resetAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST")
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	require.NoError(t, err)

	repo := &rateLimitAccountRepoStub{
		accountOnGet: &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey},
	}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Extra: map[string]any{}}

	require.True(t, svc.handle429(context.Background(), account, nil, body))
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.WithinDuration(t, resetAt.UTC(), repo.lastRateLimitedResetAt.UTC(), time.Second)
	require.Greater(t, repo.lastRateLimitedResetAt.Sub(time.Now()), 24*time.Hour)
	require.NotNil(t, repo.lastExtraUpdates)
	require.Equal(t, 1.0, repo.lastExtraUpdates[newAPIWeeklyUtilExtraKey])
	require.Equal(t, float64(resetAt.Unix()), repo.lastExtraUpdates[newAPIWeeklyResetExtraKey])
}
