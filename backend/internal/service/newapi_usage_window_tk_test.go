//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
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
			NewAPIAccountWindowLockExtraKey: 1.0,
			newAPIWeeklyUtilExtraKey:        1.0,
			newAPIWeeklyResetExtraKey:       float64(resetAt.Unix()),
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
			NewAPIAccountWindowLockExtraKey: 1.0,
			newAPIWeeklyUtilExtraKey:        1.0,
			newAPIWeeklyResetExtraKey:       float64(time.Now().Add(-time.Hour).Unix()),
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

func TestNewAPIMonthlyQuotaResponse(t *testing.T) {
	now := time.Date(2026, 9, 18, 23, 33, 0, 0, time.UTC)
	for _, reset := range []string{"2026-09-30 23:59:59 +0800 CST", "2026-10-01 23:59:59 +0800 CST"} {
		t.Run(reset, func(t *testing.T) {
			hit := tkParseNewAPIUsageWindowResponse("You have exceeded the monthly usage quota. It will reset at "+reset+". We recommend upgrading your plan for more quota, or waiting for the reset.", http.Header{"Retry-After": {"5"}}, now)
			require.NotNil(t, hit)
			require.Equal(t, "monthly", hit.Window)
			want, err := time.Parse("2006-01-02 15:04:05 -0700 MST", reset)
			require.NoError(t, err)
			require.True(t, want.Equal(hit.ResetAt))
		})
	}
	hit := tkParseNewAPIUsageWindowResponse("You have exceeded the monthly usage quota. It will reset at 10-01 23:59:59 UTC", http.Header{"Retry-After": {"5"}}, now)
	require.NotNil(t, hit)
	require.Equal(t, time.Date(2026, 10, 1, 23, 59, 59, 0, time.UTC), hit.ResetAt)
	require.Nil(t, tkParseNewAPIUsageWindowResponse("You have exceeded the monthly usage quota. It will reset at 09-01 23:59:59 UTC", nil, now))
	require.Nil(t, tkParseNewAPIUsageWindowResponse("You have exceeded the monthly usage quota.", nil, now))
	require.Nil(t, tkParseNewAPIUsageWindowResponse("Monthly requests rate limit exceeded, please retry later", http.Header{"Retry-After": {"5"}}, now))
}

func TestHandleUpstreamError_NewAPIMonthlyQuotaLifecycle(t *testing.T) {
	reset := time.Now().Add(12 * 24 * time.Hour).UTC().Truncate(time.Second)
	body, err := json.Marshal(map[string]any{"error": map[string]any{"code": "AccountQuotaExceeded", "message": "You have exceeded the monthly usage quota. It will reset at " + reset.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST") + "."}})
	require.NoError(t, err)
	for _, schedulable := range []bool{true, false} {
		account := &Account{ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: schedulable}
		repo := &rateLimitAccountRepoStub{accountOnGet: account}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, body)
		require.Equal(t, 1, repo.setRateLimitedCalls)
		require.Equal(t, reset, repo.lastRateLimitedResetAt.UTC())
		require.Equal(t, 1.0, repo.lastExtraUpdates["newapi_monthly_utilization"])
		require.Equal(t, float64(reset.Unix()), repo.lastExtraUpdates["newapi_monthly_reset"])
		require.Zero(t, repo.setErrorCalls)
		require.Equal(t, schedulable, account.Schedulable)
		account.RateLimitResetAt = &reset // persisted field as reloaded by the scheduler
		require.False(t, account.IsSchedulable())
		stats := &usagestats.AccountStats{Requests: 42}
		usage := buildLocalWindowUsageFromStats(time.Now(), stats, stats)
		applyNewAPIUsageWindowSnapshot(account, usage)
		require.True(t, usage.FiveHour.UtilizationUnknown)
		require.True(t, usage.SevenDay.UtilizationUnknown, "monthly quota must not appear as 7d quota")
		require.Equal(t, int64(42), usage.SevenDay.WindowStats.Requests)
		require.NotNil(t, usage.UpstreamQuota)
		require.Equal(t, "degraded", usage.UpstreamQuota.State)
		require.Len(t, usage.UpstreamQuota.Dimensions, 1)
		dimension := usage.UpstreamQuota.Dimensions[0]
		require.Equal(t, "newapi_monthly", dimension.Key)
		require.Equal(t, "1mo", dimension.Window)
		require.Equal(t, 100.0, *dimension.Utilization)
		require.True(t, reset.Equal(*dimension.ResetsAt))
		applyNewAPIUsageWindowSnapshot(account, usage)
		require.Len(t, usage.UpstreamQuota.Dimensions, 1)
		past := time.Now().Add(-time.Second)
		account.RateLimitResetAt = &past
		account.Extra["newapi_monthly_reset"] = float64(past.Unix())
		fresh := buildLocalWindowUsageFromStats(time.Now(), stats, stats)
		applyNewAPIUsageWindowSnapshot(account, fresh)
		require.Nil(t, fresh.UpstreamQuota)
		require.True(t, fresh.SevenDay.UtilizationUnknown)
		require.Equal(t, schedulable, account.IsSchedulable())
	}
}

const qianfanMonthlyQuotaBody = `{"error":{"code":"token_quota_exceeded","message":"Token Plan Person monthly quota limit exceeded","type":"quota_exceeded"},"id":"as-test"}`

func TestNewAPIQianfanMonthlyQuotaCalendarReset(t *testing.T) {
	for _, tc := range []struct{ now, reset string }{
		{"2026-09-19T00:23:00Z", "2026-09-30T17:00:00Z"},
		{"2026-12-31T15:59:59Z", "2026-12-31T17:00:00Z"},
		{"2026-12-31T16:00:00Z", "2027-01-31T17:00:00Z"},
		{"2026-01-31T16:00:00Z", "2026-02-28T17:00:00Z"},
		{"2028-02-01T00:00:00Z", "2028-02-29T17:00:00Z"},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			for _, retry := range []string{"", "5", "7200"} {
				hit := tkParseNewAPIUsageWindowResponse(qianfanMonthlyQuotaBody, http.Header{"Retry-After": {retry}}, now)
				require.NotNil(t, hit)
				require.Equal(t, "monthly", hit.Window)
				require.Equal(t, tc.reset, hit.ResetAt.UTC().Format(time.RFC3339))
			}
		})
	}
}

func TestNewAPIQianfanMonthlyQuotaLifecycle(t *testing.T) {
	for _, schedulable := range []bool{true, false} {
		account := &Account{ID: 130, Platform: PlatformNewAPI, ChannelType: 46, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: schedulable}
		repo := &rateLimitAccountRepoStub{accountOnGet: account}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		reset := tkQianfanMonthlyResetAt(time.Now())
		svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{"Retry-After": {"5"}}, []byte(qianfanMonthlyQuotaBody))
		require.Equal(t, 1, repo.setRateLimitedCalls)
		require.True(t, reset.Equal(repo.lastRateLimitedResetAt))
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.tempCalls)
		require.Equal(t, 1.0, repo.lastExtraUpdates[newAPIMonthlyUtilExtraKey])
		require.Equal(t, float64(reset.Unix()), repo.lastExtraUpdates[newAPIMonthlyResetExtraKey])
		require.Equal(t, schedulable, account.Schedulable)
		account.RateLimitResetAt = &reset // reloaded scheduler state
		require.False(t, account.IsSchedulable())
		usage := &UsageInfo{}
		applyNewAPIUsageWindowSnapshot(account, usage)
		require.NotNil(t, usage.UpstreamQuota)
		require.Equal(t, "degraded", usage.UpstreamQuota.State)
		require.Len(t, usage.UpstreamQuota.Dimensions, 1)
		dimension := usage.UpstreamQuota.Dimensions[0]
		require.Equal(t, "newapi_monthly", dimension.Key)
		require.Equal(t, 100.0, *dimension.Utilization)
		require.True(t, reset.Equal(*dimension.ResetsAt))
		past := time.Now().Add(-time.Second)
		account.RateLimitResetAt = &past
		account.Extra[newAPIMonthlyResetExtraKey] = float64(past.Unix())
		expired := &UsageInfo{}
		applyNewAPIUsageWindowSnapshot(account, expired)
		require.Nil(t, expired.UpstreamQuota)
		require.Equal(t, schedulable, account.IsSchedulable())
	}
}

func TestNewAPIQianfanMonthlyQuotaBoundaries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, message := range []string{"Token Plan Person daily quota limit exceeded", "token_quota_exceeded", "Monthly requests rate limit exceeded"} {
		require.Nil(t, tkParseNewAPIUsageWindowResponse(message, nil, now))
	}
	hit := tkParseNewAPIUsageWindowResponse("Token Plan Person monthly quota limit exceeded. It will reset at "+now.Add(48*time.Hour).Format("2006-01-02 15:04:05 -0700"), http.Header{"Retry-After": {"5"}}, now)
	require.NotNil(t, hit)
	require.Equal(t, "monthly", hit.Window)
	require.Equal(t, now.Add(48*time.Hour), hit.ResetAt.UTC())
}

func TestNewAPIVolcMonthlyQuotaKeepsSiblingModelsAvailable(t *testing.T) {
	reset := time.Now().Add(12 * 24 * time.Hour).UTC().Truncate(time.Second)
	message := "You have exceeded the monthly usage quota. It will reset at " + reset.Format("2006-01-02 15:04:05 -0700")
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	require.NoError(t, err)
	for _, schedulable := range []bool{true, false} {
		account := newAgentPlanRateLimitAccountForTest(newapiintegration.VolcEngineAgentPlanBaseURL)
		account.Schedulable = schedulable
		account.Credentials["model_mapping"] = map[string]any{"public-alias": "deepseek-v4-pro", "deepseek-v4-pro": "must-not-remap"}
		repo := &rateLimitAccountRepoStub{accountOnGet: account}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		gateway := &OpenAIGatewayService{rateLimitService: svc}
		svc.SetAccountRuntimeBlocker(gateway)
		gateway.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{"Retry-After": {"5"}}, body, "deepseek-v4-pro")
		require.Len(t, repo.modelRateLimitCalls, 1)
		call := repo.modelRateLimitCalls[0]
		require.Equal(t, "deepseek-v4-pro", call.scope)
		require.True(t, reset.Equal(call.resetAt))
		require.Zero(t, repo.setRateLimitedCalls)
		require.Zero(t, repo.setErrorCalls)
		require.Zero(t, repo.updateExtraCalls, "model cooldown is the sole quota observation owner")
		require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
		account.Extra = map[string]any{modelRateLimitsKey: map[string]any{call.scope: map[string]any{
			"rate_limit_reset_at": call.resetAt.Format(time.RFC3339), "reason": call.reason,
		}}}
		require.False(t, account.IsSchedulableForModelWithContext(context.Background(), "public-alias"))
		require.Equal(t, schedulable, account.IsSchedulableForModelWithContext(context.Background(), "ark-code-latest"))
		require.Equal(t, schedulable, account.IsSchedulableForModelWithContext(context.Background(), "unobserved-sibling"))
		usage := &UsageInfo{}
		applyNewAPIUsageWindowSnapshot(account, usage)
		require.NotNil(t, usage.UpstreamQuota)
		require.Len(t, usage.UpstreamQuota.Dimensions, 1)
		dim := usage.UpstreamQuota.Dimensions[0]
		require.Equal(t, "newapi_monthly:deepseek-v4-pro", dim.Key)
		require.Equal(t, "deepseek-v4-pro", dim.Label)
		require.Equal(t, "1mo", dim.Window)
		require.Equal(t, 100.0, *dim.Utilization)
		require.True(t, reset.Equal(*dim.ResetsAt))
		applyNewAPIUsageWindowSnapshot(account, usage)
		require.Len(t, usage.UpstreamQuota.Dimensions, 1)
		account.Extra[modelRateLimitsKey].(map[string]any)[call.scope].(map[string]any)["rate_limit_reset_at"] = time.Now().Add(-time.Second).Format(time.RFC3339)
		expired := &UsageInfo{}
		applyNewAPIUsageWindowSnapshot(account, expired)
		require.Nil(t, expired.UpstreamQuota)
		require.Equal(t, schedulable, account.IsSchedulableForModelWithContext(context.Background(), "public-alias"))
	}
}

func TestNewAPIVolcMonthlyQuotaCannotEscalateWriteFailure(t *testing.T) {
	reset := time.Now().Add(12 * 24 * time.Hour).UTC().Truncate(time.Second)
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": "You have exceeded the monthly usage quota. It will reset at " + reset.Format("2006-01-02 15:04:05 -0700")}})
	require.NoError(t, err)
	for _, model := range []string{"deepseek-v4-pro"} {
		account := newAgentPlanRateLimitAccountForTest(newapiintegration.VolcEngineAgentPlanBaseURL)
		repo := &rateLimitAccountRepoStub{accountOnGet: account, modelRateLimitErr: errors.New("write failed")}
		svc := NewRateLimitService(repo, nil, nil, nil, nil)
		svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, nil, body, model)
		require.Less(t, time.Until(repo.lastRateLimitedResetAt), time.Minute, "missing evidence or persistence failure must not create a month-long account block")
		require.Zero(t, repo.updateExtraCalls)
	}
}

func TestNewAPIVolcMonthlyQuotaUsesExecutedPlanModel(t *testing.T) {
	account := parameterCompatibilityAccount(PlatformNewAPI, "deepseek-v4-pro", protocolrouter.ProtocolChatCompletions)
	account.ChannelType = 45
	account.Credentials["base_url"] = newapiintegration.VolcEngineAgentPlanBaseURL
	attachTestProtocolCapability(&account, protocolrouter.ProtocolChatCompletions)
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`))
	require.NoError(t, err)
	snapshot, err := protocolAccountSnapshotForRequest(&account, request)
	require.NoError(t, err)
	plan, err := NewProtocolRouter().Plan(request, snapshot)
	require.NoError(t, err)
	ctx := withProtocolExecutionPlan(context.Background(), plan)
	repo := &rateLimitAccountRepoStub{accountOnGet: &account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	message := "You have exceeded the monthly usage quota. It will reset at " + time.Now().Add(24*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700")
	// The bridge carries no model argument: execution Plan still identifies the scope.
	tkHandleBridgeUpstreamPenalty(ctx, svc, &account, upstreamBridgeError(429, message))
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "deepseek-v4-pro", repo.modelRateLimitCalls[0].scope)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.updateExtraCalls)
}

func TestHandle429_NewAPIWeeklyWithModelIsModelScoped(t *testing.T) {
	resetAt := time.Now().Add(30 * time.Hour).UTC().Truncate(time.Second)
	msg := "You have exceeded the weekly usage quota. It will reset at " +
		resetAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST")
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	require.NoError(t, err)

	account := &Account{
		ID: 88, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Extra: map[string]any{},
	}
	account.RateLimitResetAt = &resetAt
	account.Extra[newAPIWeeklyUtilExtraKey] = 1.0
	account.Extra[newAPIWeeklyResetExtraKey] = float64(resetAt.Unix())
	require.True(t, account.IsSchedulable())
	require.False(t, account.IsRateLimited())

	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, svc.handle429(context.Background(), account, nil, body, "deepseek-v4-flash"))
	require.Zero(t, repo.setRateLimitedCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "deepseek-v4-flash", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, tkNewAPIModelWindowReason+":weekly", repo.modelRateLimitCalls[0].reason)
	require.Equal(t, &resetAt, account.RateLimitResetAt)
	require.False(t, account.IsRateLimited())
}

func TestTkParseNewAPIUsageWindowHit_Monthly(t *testing.T) {
	hit := tkParseNewAPIUsageWindowResponse(
		"You have exceeded the monthly usage quota. It will reset at 2026-10-01 00:00:00 +0800 CST",
		nil, time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
	)
	require.NotNil(t, hit)
	require.Equal(t, "monthly", hit.Window)
	require.Equal(t, time.October, hit.ResetAt.Month())
	require.False(t, tkIsAccountStandingBillingFailure("You have exceeded the monthly usage quota. It will reset at 2026-10-01 00:00:00 +0800 CST", nil))
}

func TestHandle429_VolcAgentPlanWeeklyIsModelScoped(t *testing.T) {
	resetAt := time.Now().Add(39 * time.Hour).UTC().Truncate(time.Second)
	msg := "You have exceeded the weekly usage quota. It will reset at " +
		resetAt.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05 -0700 MST")
	body, err := json.Marshal(map[string]any{"error": map[string]any{"message": msg}})
	require.NoError(t, err)

	account := volcAgentPlanUsageWindowAccount()
	account.RateLimitResetAt = &resetAt
	account.RateLimitedAt = &resetAt
	account.Extra[newAPIWeeklyUtilExtraKey] = 1.0
	account.Extra[newAPIWeeklyResetExtraKey] = float64(resetAt.Unix())
	require.True(t, account.IsSchedulable(), "a per-model weekly lock must not unschedulable the whole account")
	require.False(t, account.IsRateLimited())

	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, svc.handle429(context.Background(), account, nil, body, "kimi-k3"))
	require.Zero(t, repo.setRateLimitedCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "kimi-k3", repo.modelRateLimitCalls[0].scope)
	require.Equal(t, tkNewAPIModelWindowReason+":weekly", repo.modelRateLimitCalls[0].reason)
	require.WithinDuration(t, resetAt, repo.modelRateLimitCalls[0].resetAt, time.Second)
	require.Equal(t, &resetAt, account.RateLimitResetAt)
	require.False(t, account.IsRateLimited())
	require.Equal(t, 1.0, account.Extra[newAPIWeeklyUtilExtraKey])
	require.Zero(t, repo.updateExtraCalls)
	require.False(t, isWholeAccountRuntimeBlockReason(tkNewAPIModelWindowReason))
	got := classifyIncident(tkNewAPIModelWindowReason, resetAt, IncidentKindUnknown)
	require.Equal(t, tkNewAPIModelWindowReason, got.reasonClass)
	require.Contains(t, got.kindZh, "其它模型仍可调度")

	usage := buildLocalWindowUsageFromStats(time.Now(), &usagestats.AccountStats{Requests: 14}, &usagestats.AccountStats{Requests: 824})
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.True(t, usage.SevenDay.UtilizationUnknown)
	require.Equal(t, "newapi_weekly:kimi-k3", usage.UpstreamQuota.Dimensions[0].Key)
	require.Equal(t, "7d", usage.UpstreamQuota.Dimensions[0].Window)
	require.False(t, account.IsSchedulableForModelWithContext(context.Background(), "kimi-k3"))
	require.True(t, account.IsSchedulableForModelWithContext(context.Background(), "deepseek-v4-flash"))
}

func TestHandle429_VolcAgentPlanWithoutModelStaysAccountWide(t *testing.T) {
	resetAt := time.Now().Add(36 * time.Hour).UTC().Truncate(time.Second)
	body := []byte("You have exceeded the weekly usage quota. It will reset at " + resetAt.Format("2006-01-02 15:04:05 -0700 MST"))
	account := volcAgentPlanUsageWindowAccount()
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	require.True(t, svc.handle429(context.Background(), account, nil, body))
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.Empty(t, repo.modelRateLimitCalls)
	require.Equal(t, 1.0, account.Extra[NewAPIAccountWindowLockExtraKey])
	account.RateLimitedAt = &resetAt
	account.RateLimitResetAt = &repo.lastRateLimitedResetAt
	require.True(t, account.IsRateLimited(), "a no-model Agent Plan window must still cool the account after reload")
	require.False(t, account.IsSchedulable())
}

func volcAgentPlanUsageWindowAccount() *Account {
	return &Account{
		ID:          17,
		Name:        "volcengine-agent-plan",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL},
		Extra:       map[string]any{},
	}
}

// A failed snapshot write cannot leave a long unmarked account lock.
type failingNewAPIWindowRepo struct{ rateLimitAccountRepoStub }

func (r *failingNewAPIWindowRepo) UpdateExtra(ctx context.Context, id int64, updates map[string]any) error {
	r.rateLimitAccountRepoStub.UpdateExtra(ctx, id, updates)
	return errors.New("snapshot write failed")
}
func TestNewAPIWindowSnapshotFailureFallsBackWithoutLongAccountLock(t *testing.T) {
	account := volcAgentPlanUsageWindowAccount()
	repo := &failingNewAPIWindowRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	body := []byte("You have exceeded the weekly usage quota. It will reset at " + time.Now().Add(48*time.Hour).UTC().Format("2006-01-02 15:04:05 -0700"))
	require.False(t, svc.handle429(context.Background(), account, nil, body))
	require.Equal(t, 1, repo.updateExtraCalls)
	require.Equal(t, 1.0, repo.lastExtraUpdates[NewAPIAccountWindowLockExtraKey])
	require.Equal(t, 1.0, repo.lastExtraUpdates[newAPIWeeklyUtilExtraKey])
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.Less(t, time.Until(repo.lastRateLimitedResetAt), time.Minute)
	require.Empty(t, account.Extra)
}
func TestNewAPIQianfanMonthlyQuotaWithModel(t *testing.T) {
	account := &Account{ID: 130, Platform: PlatformNewAPI, ChannelType: 46, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	repo := &rateLimitAccountRepoStub{accountOnGet: account}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	svc.HandleUpstreamError(context.Background(), account, 429, http.Header{"Retry-After": {"5"}}, []byte(qianfanMonthlyQuotaBody), "deepseek-v4-pro")
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.updateExtraCalls)
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.True(t, tkQianfanMonthlyResetAt(time.Now()).Equal(repo.modelRateLimitCalls[0].resetAt))
	require.False(t, account.IsSchedulableForModelWithContext(context.Background(), "deepseek-v4-pro"))
	require.True(t, account.IsSchedulableForModelWithContext(context.Background(), "other-model"))
	usage := &UsageInfo{}
	applyNewAPIUsageWindowSnapshot(account, usage)
	require.Equal(t, "newapi_monthly:deepseek-v4-pro", usage.UpstreamQuota.Dimensions[0].Key)
	require.Equal(t, "1mo", usage.UpstreamQuota.Dimensions[0].Window)
}
