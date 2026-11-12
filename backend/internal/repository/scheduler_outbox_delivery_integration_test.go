//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerOutboxDeliversSmallIDCommittedAfterLargeACK(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `TRUNCATE scheduler_outbox`)
	require.NoError(t, err)
	repo := &schedulerOutboxRepository{db: integrationDB}
	late, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = late.Rollback() }()
	var small, large int64
	require.NoError(t, late.QueryRowContext(ctx, `INSERT INTO scheduler_outbox (event_type, created_at) VALUES ('account_changed', NOW() - interval '1 minute') RETURNING id`).Scan(&small))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO scheduler_outbox (event_type) VALUES ('account_changed') RETURNING id`).Scan(&large))
	events, err := repo.ClaimPending(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, large, events[0].ID)
	ok, err := repo.Acknowledge(ctx, large, events[0].ClaimToken)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, late.Commit())
	// The old watermark cleanup must not delete the newly visible smaller ID.
	_, err = repo.DeleteConsumedUpTo(ctx, large, 100)
	require.NoError(t, err)
	pending, err := repo.PendingCount(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, pending)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE id = $1`, small)
	require.ErrorContains(t, err, "requires a delivery ACK")
	events, err = repo.ClaimPending(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, small, events[0].ID)
	ok, err = repo.Acknowledge(ctx, small, events[0].ClaimToken)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSchedulerOutboxExpiredClaimCannotACKNewOwner(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `TRUNCATE scheduler_outbox; INSERT INTO scheduler_outbox (event_type) VALUES ('account_changed')`)
	require.NoError(t, err)
	repo := &schedulerOutboxRepository{db: integrationDB}
	first, err := repo.ClaimPending(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, first, 1)
	second, err := repo.ClaimPending(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Empty(t, second)
	_, err = integrationDB.ExecContext(ctx, `UPDATE scheduler_outbox SET lease_until = clock_timestamp() - interval '1 second'`)
	require.NoError(t, err)
	ok, err := repo.Acknowledge(ctx, first[0].ID, first[0].ClaimToken)
	require.NoError(t, err)
	require.False(t, ok)
	second, err = repo.ClaimPending(ctx, 10, time.Minute)
	require.NoError(t, err)
	require.Len(t, second, 1)
	require.Equal(t, 2, second[0].Attempts)
	require.NotEqual(t, first[0].ClaimToken, second[0].ClaimToken)
	ok, err = repo.Acknowledge(ctx, first[0].ID, first[0].ClaimToken)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = repo.Acknowledge(ctx, second[0].ID, second[0].ClaimToken)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSchedulerOutboxDeliverySerializesWorkersAndCancelsOnConnectionLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := &schedulerOutboxRepository{db: integrationDB}
	lease, acquired, err := repo.TryAcquireDeliveryLock(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	defer lease.Release()
	cache := NewSchedulerCache(testRedis(t)).(*schedulerCache)
	oldCtx, err := cache.BeginOutboxProjection(context.Background(), lease.Generation())
	require.NoError(t, err)
	queued := cache.rdb.Pipeline()
	queued.Set(oldCtx, schedulerAccountKey("190"), `{"id":190,"name":"stale"}`, 0)
	_, acquired, err = repo.TryAcquireDeliveryLock(ctx)
	require.NoError(t, err)
	require.False(t, acquired)
	var pid int
	require.NoError(t, lease.(*schedulerDeliveryLease).conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
	_, err = integrationDB.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, pid)
	require.NoError(t, err)
	select {
	case <-lease.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lost advisory lock must cancel projection work")
	}
	next, acquired, err := repo.TryAcquireDeliveryLock(ctx)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Greater(t, next.Generation(), lease.Generation())
	newCtx, err := cache.BeginOutboxProjection(ctx, next.Generation())
	require.NoError(t, err)
	require.NoError(t, cache.SetAccount(newCtx, &service.Account{ID: 190, Name: "current", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}))
	// The old context deliberately stays alive: correctness cannot rely on the
	// 250ms connection-loss monitor or cooperative cancellation alone.
	_, err = cache.execSchedulerPipeline(oldCtx, queued)
	require.ErrorIs(t, err, errSchedulerProjectionFenced)
	require.ErrorIs(t, cache.DeleteAccount(oldCtx, 190), errSchedulerProjectionFenced)
	_, err = cache.BeginOutboxProjection(ctx, lease.Generation())
	require.ErrorIs(t, err, errSchedulerProjectionFenced)
	account, err := cache.GetAccount(ctx, 190)
	require.NoError(t, err)
	require.Equal(t, "current", account.Name)
	next.Release()
}
