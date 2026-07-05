//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func tkUserBalance(t *testing.T, userID int64) float64 {
	t.Helper()
	var balance float64
	require.NoError(t, integrationDB.QueryRow(
		`SELECT balance FROM users WHERE id = $1`, userID).Scan(&balance))
	return balance
}

func tkAPIKeyQuotaState(t *testing.T, apiKeyID int64) (float64, string) {
	t.Helper()
	var used float64
	var status string
	require.NoError(t, integrationDB.QueryRow(
		`SELECT quota_used, status FROM api_keys WHERE id = $1`, apiKeyID).Scan(&used, &status))
	return used, status
}

// TestUsageBillingRepositoryApplyVideoRefund_BalanceRoundTrip exercises the
// full forward-charge → terminal-failure-refund cycle on real PG: the refund
// returns the balance, releases api-key quota (re-activating an exhausted
// key), and applies at most once.
func TestUsageBillingRepositoryApplyVideoRefund_BalanceRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("video-refund-user-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      100,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-video-refund-" + uuid.NewString(),
		Name:   "video-refund",
		Quota:  1, // forward charge of 0.87 + quota 1 → not exhausted; second charge would be
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "video-refund-account-" + uuid.NewString(),
		Type: domain.AccountTypeAPIKey,
	})

	// Forward charge (what video submit billing applies).
	forward := &service.UsageBillingCommand{
		RequestID:       "local:" + uuid.NewString(),
		APIKeyID:        apiKey.ID,
		UserID:          user.ID,
		AccountID:       account.ID,
		AccountType:     domain.AccountTypeAPIKey,
		BalanceCost:     0.87,
		APIKeyQuotaCost: 0.87,
	}
	res, err := repo.Apply(ctx, forward)
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.InDelta(t, 100-0.87, tkUserBalance(t, user.ID), 1e-9)
	used, _ := tkAPIKeyQuotaState(t, apiKey.ID)
	require.InDelta(t, 0.87, used, 1e-9)

	// Terminal-failure refund.
	refund := &service.VideoRefundCommand{
		RequestID:   service.TkVideoRefundRequestIDPrefix + "vt_" + uuid.NewString(),
		UserID:      user.ID,
		APIKeyID:    apiKey.ID,
		BillingType: service.BillingTypeBalance,
		Amount:      0.87,
	}
	applied, err := repo.ApplyVideoRefund(ctx, refund)
	require.NoError(t, err)
	require.True(t, applied)
	require.InDelta(t, 100, tkUserBalance(t, user.ID), 1e-9, "balance must round-trip to the pre-charge value")
	used, status := tkAPIKeyQuotaState(t, apiKey.ID)
	require.InDelta(t, 0, used, 1e-9, "api-key quota must be released")
	require.Equal(t, service.StatusAPIKeyActive, status)

	// Second application (concurrent poll / retried fetch) must be a no-op.
	applied, err = repo.ApplyVideoRefund(ctx, refund)
	require.NoError(t, err)
	require.False(t, applied)
	require.InDelta(t, 100, tkUserBalance(t, user.ID), 1e-9, "idempotent refund must not double-credit")
}

// TestUsageBillingRepositoryApplyVideoRefund_ReactivatesExhaustedKey locks the
// symmetric un-exhaust: a key the forward charge flipped to quota-exhausted
// comes back to active when the refund drops usage below the quota again.
func TestUsageBillingRepositoryApplyVideoRefund_ReactivatesExhaustedKey(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("video-refund-exhaust-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Balance:      10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-video-refund-exhaust-" + uuid.NewString(),
		Name:   "video-refund-exhaust",
		Quota:  1,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "video-refund-exhaust-account-" + uuid.NewString(),
		Type: domain.AccountTypeAPIKey,
	})

	forward := &service.UsageBillingCommand{
		RequestID:       "local:" + uuid.NewString(),
		APIKeyID:        apiKey.ID,
		UserID:          user.ID,
		AccountID:       account.ID,
		AccountType:     domain.AccountTypeAPIKey,
		BalanceCost:     1.25,
		APIKeyQuotaCost: 1.25, // > quota 1 → exhausts the key
	}
	res, err := repo.Apply(ctx, forward)
	require.NoError(t, err)
	require.True(t, res.Applied)
	require.True(t, res.APIKeyQuotaExhausted)
	_, status := tkAPIKeyQuotaState(t, apiKey.ID)
	require.Equal(t, service.StatusAPIKeyQuotaExhausted, status)

	applied, err := repo.ApplyVideoRefund(ctx, &service.VideoRefundCommand{
		RequestID:   service.TkVideoRefundRequestIDPrefix + "vt_" + uuid.NewString(),
		UserID:      user.ID,
		APIKeyID:    apiKey.ID,
		BillingType: service.BillingTypeBalance,
		Amount:      1.25,
	})
	require.NoError(t, err)
	require.True(t, applied)
	used, status := tkAPIKeyQuotaState(t, apiKey.ID)
	require.InDelta(t, 0, used, 1e-9)
	require.Equal(t, service.StatusAPIKeyActive, status, "refund below quota must re-activate the key")
}

// TestUsageBillingRepositoryApplyVideoRefund_SubscriptionFloor verifies the
// subscription rollback floors at 0 instead of going negative when the usage
// window rolled over between charge and refund.
func TestUsageBillingRepositoryApplyVideoRefund_SubscriptionFloor(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB).(*usageBillingRepository)

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("video-refund-sub-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-video-refund-sub-" + uuid.NewString(),
		Name:   "video-refund-sub",
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name: "video-refund-sub-group-" + uuid.NewString(),
	})
	// Counters simulate a rolled-over window: they hold less than the refund.
	sub := mustCreateSubscription(t, client, &service.UserSubscription{
		UserID:          user.ID,
		GroupID:         group.ID,
		DailyUsageUSD:   0.10,
		WeeklyUsageUSD:  0.50,
		MonthlyUsageUSD: 2.00,
	})

	applied, err := repo.ApplyVideoRefund(ctx, &service.VideoRefundCommand{
		RequestID:      service.TkVideoRefundRequestIDPrefix + "vt_" + uuid.NewString(),
		UserID:         user.ID,
		APIKeyID:       apiKey.ID,
		SubscriptionID: &sub.ID,
		BillingType:    service.BillingTypeSubscription,
		Amount:         0.87,
	})
	require.NoError(t, err)
	require.True(t, applied)

	var daily, weekly, monthly float64
	require.NoError(t, integrationDB.QueryRow(`
		SELECT daily_usage_usd, weekly_usage_usd, monthly_usage_usd
		FROM user_subscriptions WHERE id = $1`, sub.ID).Scan(&daily, &weekly, &monthly))
	require.InDelta(t, 0, daily, 1e-9, "daily floors at 0, never negative")
	require.InDelta(t, 0, weekly, 1e-9)
	require.InDelta(t, 2.00-0.87, monthly, 1e-9)
}
