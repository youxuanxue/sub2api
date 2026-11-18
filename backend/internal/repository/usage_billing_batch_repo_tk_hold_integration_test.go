//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBillingLaneAndBatchPreserveHandedOffHolds(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	raw := NewUsageBillingRepository(client, integrationDB)
	lanes := service.NewUsageBillingLaneCombiner(raw)
	defer lanes.Stop()
	var decorated service.UsageBillingRepository = lanes
	holds, ok := decorated.(service.UsageBillingHoldApplier)
	require.True(t, ok, "DI decorator must preserve hold admission/reconciliation")
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash", Balance: 10})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "batch-hold"})
	cmds := make([]*service.UsageBillingCommand, 2)
	for i, cost := range []float64{3, 7} {
		holdID := uuid.NewString()
		reserved, err := holds.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: holdID, UserID: user.ID, APIKeyID: key.ID, Amount: 2})
		require.NoError(t, err)
		require.True(t, reserved)
		cmds[i] = &service.UsageBillingCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: key.ID, BalanceCost: cost, TkHoldRequestID: holdID}
	}
	require.InDelta(t, 6, holdBalance(t, user.ID), 1e-9)
	out, err := raw.ApplyBatch(ctx, cmds)
	require.NoError(t, err)
	require.Len(t, out, 2)
	for i, wantBalance := range []float64{5, 0} {
		require.NoError(t, out[i].Err)
		require.True(t, out[i].Result.Applied)
		require.InDelta(t, wantBalance, *out[i].Result.NewBalance, 1e-9)
		require.False(t, out[i].Result.BalanceOverdrafted)
		released, err := holds.ReleaseBalanceHold(ctx, cmds[i].TkHoldRequestID)
		require.NoError(t, err)
		require.False(t, released, "settlement already consumed hold")
	}
	require.InDelta(t, 0, holdBalance(t, user.ID), 1e-9)
	again, err := raw.ApplyBatch(ctx, cmds)
	require.NoError(t, err)
	for _, result := range again {
		require.False(t, result.Result.Applied)
	}
	require.InDelta(t, 0, holdBalance(t, user.ID), 1e-9, "retry cannot refund or charge twice")
}
