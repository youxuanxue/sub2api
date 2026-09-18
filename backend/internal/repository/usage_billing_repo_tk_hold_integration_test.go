//go:build integration

package repository

import (
	"context"
	"database/sql"
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

// The release-before-settle closure: a hold handed off to settlement must be
// consumed (refunded) in the SAME billing transaction as the deduction, so the
// balance moves B → B + hold − actual in one atomic step and is never exposed
// at B + hold with the bill still pending.
func TestApply_ConsumesHandedOffHoldAtomically(t *testing.T) {
	for _, actual := range []float64{3, 7} {
		t.Run(fmt.Sprintf("actual-%g", actual), func(t *testing.T) {
			ctx := context.Background()
			client := testEntClient(t)
			repo := NewUsageBillingRepository(client, integrationDB)
			applier, ok := repo.(service.UsageBillingHoldApplier)
			require.True(t, ok)

			user := mustCreateUser(t, client, &service.User{
				Email:        fmt.Sprintf("hold-settle-%s@example.com", uuid.NewString()),
				PasswordHash: "hash",
				Balance:      10,
			})
			apiKey := mustCreateApiKey(t, client, &service.APIKey{
				UserID: user.ID,
				Key:    "sk-hold-settle-" + uuid.NewString(),
				Name:   "hold-settle",
				Quota:  1,
			})

			holdID := "local:" + uuid.NewString()
			reserved, err := applier.ReserveBalanceHold(ctx, &service.HoldCommand{
				RequestID: holdID, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 5,
			})
			require.NoError(t, err)
			require.True(t, reserved)
			require.InDelta(t, 5, holdBalance(t, user.ID), 1e-9)

			cmd := &service.UsageBillingCommand{
				RequestID:       uuid.NewString(), // billing id may differ from the hold id (WS turns)
				APIKeyID:        apiKey.ID,
				UserID:          user.ID,
				AccountID:       1,
				AccountType:     service.AccountTypeAPIKey,
				Model:           "test-model",
				BalanceCost:     actual,
				TkHoldRequestID: holdID,
			}
			cmd.Normalize()
			result, err := repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.InDelta(t, 10-actual, holdBalance(t, user.ID), 1e-9, "settlement reconciles both below-hold and above-hold charges")
			result, err = repo.Apply(ctx, cmd)
			require.NoError(t, err)
			require.False(t, result.Applied, "duplicate settlement must not deduct twice")
			require.InDelta(t, 10-actual, holdBalance(t, user.ID), 1e-9)

			var n int
			require.NoError(t, integrationDB.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM usage_holds WHERE request_id = $1", holdID).Scan(&n))
			require.Equal(t, 0, n, "consumed hold row must be gone")

			// A late handler-end release must find nothing to refund.
			released, err := applier.ReleaseBalanceHold(ctx, holdID)
			require.NoError(t, err)
			require.False(t, released)
			require.InDelta(t, 10-actual, holdBalance(t, user.ID), 1e-9)
		})
	}
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

// A release already owns the last DB connection while a new reservation queues.
// Both must complete: an application mutex held across pool acquisition would
// invert the order and block the release behind the waiting reservation.
func TestBalanceHold_ProgressWithExhaustedConnectionPool(t *testing.T) {
	applier, user, apiKey := newHoldApplier(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	holdID := uuid.NewString()
	ok, err := applier.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: holdID, UserID: user.ID, APIKeyID: apiKey.ID, Amount: 1})
	require.NoError(t, err)
	require.True(t, ok)

	gate, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer gate.Close()
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_lock($1)", user.ID)
	require.NoError(t, err)
	defer func() { _, _ = gate.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", user.ID) }()
	function := fmt.Sprintf("hold_release_gate_%d", user.ID)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS
		'BEGIN PERFORM pg_advisory_xact_lock(OLD.user_id); RETURN OLD; END';
		CREATE TRIGGER %s BEFORE DELETE ON usage_holds FOR EACH ROW
		WHEN (OLD.user_id = %d) EXECUTE FUNCTION %s();`, function, function, user.ID, function))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(context.Background(), fmt.Sprintf("DROP TRIGGER %s ON usage_holds; DROP FUNCTION %s()", function, function))
		require.NoError(t, err)
	})

	pool, err := sql.Open("postgres", integrationPostgresDSN)
	require.NoError(t, err)
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	repo := &usageBillingRepository{db: pool}
	released := make(chan error, 1)
	go func() { _, err := repo.ReleaseBalanceHold(ctx, holdID); released <- err }()
	require.Eventually(t, func() bool {
		var waiting int
		err := integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND objid = $1", user.ID).Scan(&waiting)
		return err == nil && waiting > 0
	}, time.Second, time.Millisecond)

	reserved := make(chan error, 1)
	go func() {
		ok, err := repo.ReserveBalanceHold(ctx, &service.HoldCommand{RequestID: uuid.NewString(), UserID: user.ID, APIKeyID: apiKey.ID, Amount: 1})
		if err == nil && !ok {
			err = fmt.Errorf("reservation unexpectedly rejected")
		}
		reserved <- err
	}()
	require.Eventually(t, func() bool { return pool.Stats().WaitCount > 0 }, time.Second, time.Millisecond)
	_, err = gate.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", user.ID)
	require.NoError(t, err)
	for _, completed := range []<-chan error{released, reserved} {
		select {
		case err := <-completed:
			require.NoError(t, err)
		case <-time.After(time.Second):
			cancel()
			t.Fatal("balance operation stalled behind the exhausted connection pool")
		}
	}
	require.InDelta(t, 9, holdBalance(t, user.ID), 1e-9)
}
