//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestManagedBatchQueue_DuplicateDeliveryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	q := NewManagedBatchQueue(redis.NewClient(&redis.Options{Addr: mr.Addr()}), ManagedBatchQueueOptions{Prefix: "test:managed"})
	job := service.ManagedBatchJob{ID: "row-1", Payload: []byte(`{"model":"gpt"}`)}
	require.NoError(t, q.Enqueue(ctx, job))
	require.ErrorIs(t, q.Enqueue(ctx, job), service.ErrManagedBatchAlreadyQueued)
	delivery, err := q.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, job, delivery.Job)
	require.NoError(t, q.Ack(ctx, job.ID))
	// Once acknowledged, a replay can be enqueued again.
	require.NoError(t, q.Enqueue(ctx, job))
}

func TestManagedBatchQueue_LeaseExpiryAllowsRecovery(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	q := NewManagedBatchQueue(rdb, ManagedBatchQueueOptions{Prefix: "test:managed", LeaseTTL: time.Second})
	job := service.ManagedBatchJob{ID: "row-restart", Payload: []byte("payload")}
	require.NoError(t, q.Enqueue(ctx, job))
	_, err := q.Reserve(ctx, time.Second)
	require.NoError(t, err)
	lease, ok, err := q.TryAcquireJobLease(ctx, job.ID, time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	mr.FastForward(2 * time.Second)
	second, ok, err := q.TryAcquireJobLease(ctx, job.ID, time.Second)
	require.NoError(t, err)
	require.True(t, ok, "expired worker lease must be recoverable after restart")
	require.Error(t, lease.Release(ctx), "old owner cannot release successor lease")
	require.NoError(t, second.Release(ctx))
}

func TestManagedBatchQueue_StaleActiveIsRequeuedAfterWorkerKill(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	q := NewManagedBatchQueue(rdb, ManagedBatchQueueOptions{Prefix: "test:managed"})
	job := service.ManagedBatchJob{ID: "row-kill", Payload: []byte("payload")}
	require.NoError(t, q.Enqueue(ctx, job))
	_, err := q.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.NoError(t, rdb.ZAdd(ctx, "test:managed:active", redis.Z{Score: float64(time.Now().Add(-time.Hour).UnixMilli()), Member: job.ID}).Err())
	moved, err := q.RecoverStale(ctx, time.Minute, 10)
	require.NoError(t, err)
	require.Equal(t, 1, moved)
	recovered, err := q.Reserve(ctx, time.Second)
	require.NoError(t, err)
	require.Equal(t, job, recovered.Job)
}
