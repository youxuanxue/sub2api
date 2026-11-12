//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerProjectionFenceRejectsStaleAccountAndBucketWrites(t *testing.T) {
	cache := newSchedulerCacheUnit(t)
	ctx := context.Background()
	old, err := cache.BeginOutboxProjection(ctx, 1)
	require.NoError(t, err)
	bucket := service.SchedulerBucket{GroupID: 5, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(old, bucket)
	require.NoError(t, err)
	current, err := cache.BeginOutboxProjection(ctx, 2)
	require.NoError(t, err)
	account := service.Account{ID: 150, Name: "current", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	require.NoError(t, cache.SetSnapshot(current, bucket, token, []service.Account{account}))
	account.Name = "stale"
	require.ErrorIs(t, cache.SetAccount(old, &account), errSchedulerProjectionFenced)
	require.ErrorIs(t, cache.DeleteAccount(old, account.ID), errSchedulerProjectionFenced)
	require.ErrorIs(t, cache.UpdateLastUsed(old, map[int64]time.Time{account.ID: time.Now()}), errSchedulerProjectionFenced)
	require.ErrorIs(t, cache.SetSnapshot(old, bucket, token, []service.Account{account}), errSchedulerProjectionFenced)
	require.ErrorIs(t, cache.RetireBucket(old, bucket), errSchedulerProjectionFenced)
	_, err = cache.ReopenBucket(old, bucket)
	require.ErrorIs(t, err, errSchedulerProjectionFenced)
	_, err = cache.BeginOutboxProjection(ctx, 1)
	require.ErrorIs(t, err, errSchedulerProjectionFenced)
	got, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.Equal(t, "current", got.Name)
	_, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
}
