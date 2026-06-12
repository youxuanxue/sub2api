//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func newHoldApplier(t *testing.T) (service.UsageBillingHoldApplier, *service.User, *service.APIKey) {
	t.Helper()
	client := testEntClient(t)
	repo := NewUsageBillingRepository(client, integrationDB)
	applier, ok := repo.(service.UsageBillingHoldApplier)
	require.True(t, ok, "usage billing repository must implement UsageBillingHoldApplier")

	user := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("hold-user-%s@example.com", uuid.NewString()),
		PasswordHash: "hash",
		Balance:      10,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-hold-" + uuid.NewString(),
		Name:   "hold",
		Quota:  1,
	})
	return applier, user, apiKey
}

func holdBalance(t *testing.T, userID int64) float64 {
	t.Helper()
	var b float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		"SELECT balance FROM users WHERE id = $1", userID).Scan(&b))
	return b
}

func TestReserveBalanceHold_FloorIdempotentAndRelease(t *testing.T) {
	ctx := context.Background()
	applier, user, apiKey := newHoldApplier(t)

	reqA := uuid.NewString()
	ok, err := applier.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: reqA, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 5})
	require.NoError(t, err)
	require.True(t, ok, "5 of 10 must reserve")
	require.InDelta(t, 5, holdBalance(t, user.ID), 1e-9)

	// Floor: 6 of remaining 5 must be refused, balance untouched, no orphan hold.
	reqB := uuid.NewString()
	ok, err = applier.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: reqB, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 6})
	require.NoError(t, err)
	require.False(t, ok, "6 of 5 remaining must be refused")
	require.InDelta(t, 5, holdBalance(t, user.ID), 1e-9)

	// Idempotent: re-reserving the same request must not double-deduct.
	ok, err = applier.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: reqA, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 5})
	require.NoError(t, err)
	require.True(t, ok)
	require.InDelta(t, 5, holdBalance(t, user.ID), 1e-9)

	// Release refunds exactly once.
	released, err := applier.ReleaseBalanceHold(ctx, reqA)
	require.NoError(t, err)
	require.True(t, released)
	require.InDelta(t, 10, holdBalance(t, user.ID), 1e-9)

	released, err = applier.ReleaseBalanceHold(ctx, reqA)
	require.NoError(t, err)
	require.False(t, released, "double release must be a no-op")
	require.InDelta(t, 10, holdBalance(t, user.ID), 1e-9)
}

// The headline guarantee: concurrent reservations against a thin balance can
// never drive it negative — exactly the affordable number succeed.
func TestReserveBalanceHold_ConcurrentNeverNegative(t *testing.T) {
	ctx := context.Background()
	applier, user, apiKey := newHoldApplier(t) // balance 10

	const goroutines = 50
	var success int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			ok, err := applier.ReserveBalanceHold(ctx, &service.HoldCommand{
				RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: apiKey.ID, Amount: 1,
			})
			if err == nil && ok {
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	bal := holdBalance(t, user.ID)
	require.GreaterOrEqual(t, bal, 0.0, "balance must NEVER go negative")
	require.LessOrEqual(t, success, int64(10), "at most 10 of $1 holds can fit in $10")
	require.InDelta(t, 10-float64(success), bal, 1e-9, "balance must equal 10 minus successful holds")
}

func TestReleaseExpiredBalanceHolds_RefundsLeaks(t *testing.T) {
	ctx := context.Background()
	applier, user, apiKey := newHoldApplier(t)

	reqX := uuid.NewString()
	ok, err := applier.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: reqX, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 3})
	require.NoError(t, err)
	require.True(t, ok)
	require.InDelta(t, 7, holdBalance(t, user.ID), 1e-9)

	// Simulate a leaked hold (request crashed before release): age it past TTL.
	_, err = integrationDB.ExecContext(ctx,
		"UPDATE usage_holds SET created_at = NOW() - INTERVAL '1 hour' WHERE request_id = $1", reqX)
	require.NoError(t, err)

	refunded, err := applier.ReleaseExpiredBalanceHolds(ctx, time.Now().Add(-30*time.Minute), 100)
	require.NoError(t, err)
	require.Equal(t, 1, refunded)
	require.InDelta(t, 10, holdBalance(t, user.ID), 1e-9, "reconciler must refund the leaked hold")

	// Row is gone; a late release is a harmless no-op.
	var n int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM usage_holds WHERE request_id = $1", reqX).Scan(&n))
	require.Equal(t, 0, n)
}
