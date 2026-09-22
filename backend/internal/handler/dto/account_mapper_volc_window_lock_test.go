package dto

import (
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountFromServiceShallow_HidesVolcAgentPlanFalseWindowLock(t *testing.T) {
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	account := &service.Account{
		ID:          17,
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeVolcEngine,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL},
		Extra: map[string]any{
			"newapi_weekly_utilization": 1.0,
			"newapi_weekly_reset":       float64(reset.Unix()),
		},
		RateLimitedAt:    &reset,
		RateLimitResetAt: &reset,
	}
	got := AccountFromServiceShallow(account)
	require.Nil(t, got.RateLimitedAt)
	require.Nil(t, got.RateLimitResetAt)

	account.Extra["newapi_account_window_lock"] = 1.0
	got = AccountFromServiceShallow(account)
	require.Equal(t, &reset, got.RateLimitedAt)
	require.Equal(t, &reset, got.RateLimitResetAt)
}

func TestAccountFromServiceShallow_ProjectsModelRateLimitCascade(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	limits := map[string]any{}
	for i, model := range []string{"m1", "m2", "m3", "m4"} {
		limits[model] = map[string]any{
			"rate_limited_at":     now.Add(-time.Hour).Format(time.RFC3339),
			"rate_limit_reset_at": now.Add(time.Duration(i+1) * time.Hour).Format(time.RFC3339),
		}
	}
	account := &service.Account{
		ID:          129,
		Name:        "ali-token-plan",
		Platform:    service.PlatformNewAPI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Extra:       map[string]any{"model_rate_limits": limits},
	}
	got := AccountFromServiceShallow(account)
	require.NotNil(t, got.RateLimitResetAt)
	require.WithinDuration(t, now.Add(time.Hour), *got.RateLimitResetAt, time.Second)
	require.NotNil(t, got.RateLimitedAt)
}
