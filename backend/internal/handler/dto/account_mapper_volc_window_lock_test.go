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
