//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAISaturationCounterCache_IncrAndBatch(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	cache := NewOpenAISaturationCounterCache(rdb)
	ctx := context.Background()

	c, incErr := cache.IncrementSaturation(ctx, 7, 90)
	require.NoError(t, incErr)
	require.Equal(t, int64(1), c)

	batch, batchErr := cache.GetSaturationBatch(ctx, []int64{7, 8}, 90)
	require.NoError(t, batchErr)
	require.Equal(t, int64(1), batch[7])
	require.NotContains(t, batch, int64(8))
}

func TestOpenAISaturationCounterCache_RollingWindow(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewOpenAISaturationCounterCache(rdb).(*openaiSaturationCounterCache)
	ctx := context.Background()

	old := float64(time.Now().Add(-91 * time.Second).UnixMilli())
	require.NoError(t, rdb.ZAdd(ctx, openaiSaturationKey(7), redis.Z{Score: old, Member: "old"}).Err())
	count, err := cache.IncrementSaturation(ctx, 7, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)

	require.NoError(t, err)
	require.NoError(t, rdb.ZAdd(ctx, openaiSaturationKey(7), redis.Z{Score: old, Member: "old-read"}).Err())

	batch, err := cache.GetSaturationBatch(ctx, []int64{7}, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), batch[7], "events older than the rolling window must expire independently")
}
