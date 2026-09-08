//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestNewAPIUsageWindowAliResponse(t *testing.T) {
	now := time.Date(2026, 9, 8, 1, 28, 38, 0, time.UTC)
	message := "Your token-plan 1-week quota has been exhausted. The quota will reset at 09-12 05:58:00 UTC."
	want := time.Date(2026, 9, 12, 5, 58, 0, 0, time.UTC)
	for _, headers := range []http.Header{
		nil,
		{"Date": {now.Format(http.TimeFormat)}, "Retry-After": {"361762"}},
		{"Retry-After": {want.Format(http.TimeFormat)}},
		{"Retry-After": {"invalid"}},
	} {
		hit := tkParseNewAPIUsageWindowResponse(message, headers, now)
		require.NotNil(t, hit)
		require.Equal(t, "weekly", hit.Window)
		require.Equal(t, want, hit.ResetAt)
	}
	// A valid header remains usable even when the prose reset format changes.
	hit := tkParseNewAPIUsageWindowResponse("Your token-plan 1-week quota has been exhausted.",
		http.Header{"Date": {now.Format(http.TimeFormat)}, "Retry-After": {"361762"}}, now.Add(time.Second))
	require.NotNil(t, hit)
	require.Equal(t, want, hit.ResetAt)
}

func TestNewAPIUsageWindowYearlessBounds(t *testing.T) {
	now := time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)
	hit := tkParseNewAPIUsageWindowResponse("Your token-plan 1-week quota has been exhausted. The quota will reset at 01-02 05:58:00 UTC.", nil, now)
	require.NotNil(t, hit)
	require.Equal(t, 2027, hit.ResetAt.Year())
	for _, message := range []string{
		"Your token-plan 1-week quota has been exhausted. The quota will reset at 12-29 05:58:00 UTC.",
		"Your token-plan 1-week quota has been exhausted. The quota will reset at 02-30 05:58:00 UTC.",
		"Your token-plan 1-week quota has been exhausted. The quota will reset at 01-20 05:58:00 UTC.",
		"5-hour request burst. Please slow down.",
		"Insufficient Balance. Please recharge your account.",
	} {
		require.Nil(t, tkParseNewAPIUsageWindowResponse(message, nil, now), message)
	}
	require.Nil(t, tkParseNewAPIUsageWindowResponse("5-hour request burst. Please slow down.", http.Header{"Retry-After": {"3600"}}, now))
}

func TestHandle429_AliQuotaRecoveryPreservesManualPause(t *testing.T) {
	resetAt := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	body := []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"Your token-plan 1-week quota has been exhausted."}}`)
	account := &Account{ID: 129, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false}
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, svc.handle429(context.Background(), account, http.Header{"Retry-After": {resetAt.Format(http.TimeFormat)}}, body))
	require.Equal(t, resetAt, repo.lastRateLimitedResetAt.UTC())
	require.False(t, account.Schedulable)
	require.Equal(t, StatusActive, account.Status)
	stats := &usagestats.AccountStats{Requests: 42}
	usage := buildLocalWindowUsageFromStats(time.Now(), stats, stats)
	require.True(t, usage.SevenDay.UtilizationUnknown)
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.False(t, usage.SevenDay.UtilizationUnknown)
	require.Equal(t, 100.0, usage.SevenDay.Utilization)
	require.True(t, usage.FiveHour.UtilizationUnknown)

	// Use the existing expiration/scheduler path; recovery must never enable a
	// manually paused account or infer a measured zero from an expired snapshot.
	past := time.Now().Add(-time.Second)
	account.RateLimitResetAt = &past
	account.Extra[newAPIWeeklyResetExtraKey] = float64(past.Unix())
	usage = buildLocalWindowUsageFromStats(time.Now(), stats, stats)
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.True(t, usage.SevenDay.UtilizationUnknown)
	require.Nil(t, usage.UpstreamQuota)
	require.False(t, account.IsRateLimited())
	require.False(t, account.IsSchedulable())
	account.Schedulable = true
	require.True(t, account.IsSchedulable())
}

func TestHandle429_NewAPIPlainTextWindow(t *testing.T) {
	resetAt := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	body := []byte("You have exceeded the weekly usage quota. It will reset at " + resetAt.Format("2006-01-02 15:04:05 -0700 MST"))
	account := &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey}
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, svc.handle429(context.Background(), account, nil, body))
	require.Equal(t, resetAt, repo.lastRateLimitedResetAt.UTC())
	require.Equal(t, 1.0, account.Extra[newAPIWeeklyUtilExtraKey])
}

func TestTkParseNewAPIUsageWindowHit_WeeklyWithReset(t *testing.T) {
	hit := tkParseNewAPIUsageWindowResponse(
		"You have exceeded the weekly usage quota. It will reset at 2026-09-07 00:00:00 +0800 CST",
		nil, time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC),
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
	require.Nil(t, tkParseNewAPIUsageWindowResponse(
		"System protection triggered by request burst. Please slow down traffic growth",
		nil, time.Now(),
	))
	require.Nil(t, tkParseNewAPIUsageWindowResponse(
		"Insufficient Balance. Please recharge your account",
		nil, time.Now(),
	))
	require.Nil(t, tkParseNewAPIUsageWindowResponse(
		"You have exceeded the weekly usage quota.", // no reset timestamp
		nil, time.Now(),
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

	require.True(t, svc.tkTryHandleNewAPIUsageWindow429(context.Background(), account, nil, body))
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
