//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newSaturationTestCache(t *testing.T) (*anthropicSaturationCounterCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &anthropicSaturationCounterCache{rdb: rdb}, mr
}

func TestAnthropicSaturationCounter_IncrementAndGet(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	ctx := context.Background()

	// Empty key reads 0 via the batch read.
	zero, err := cache.GetSaturationBatch(ctx, []int64{7}, 90)
	require.NoError(t, err)
	require.Empty(t, zero, "absent key must not appear")

	// Three increments => count 3, batch read reflects it without mutating.
	for i := 1; i <= 3; i++ {
		c, incErr := cache.IncrementSaturation(ctx, 7, 90)
		require.NoError(t, incErr)
		require.Equal(t, int64(i), c)
	}
	got, err := cache.GetSaturationBatch(ctx, []int64{7, 8}, 90)
	require.NoError(t, err)
	require.Equal(t, int64(3), got[7])
	_, has8 := got[8] // a different account is independent
	require.False(t, has8)
}

func TestAnthropicSaturationCounter_RollingWindowExpiresIndividualEvents(t *testing.T) {
	cache, mr := newSaturationTestCache(t)
	rdb := cache.rdb
	ctx := context.Background()

	old := float64(time.Now().Add(-91 * time.Second).UnixMilli())
	require.NoError(t, rdb.ZAdd(ctx, anthropicSaturationKey(42), redis.Z{Score: old, Member: "old"}).Err())
	count, err := cache.IncrementSaturation(ctx, 42, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "increment must prune the expired event")
	ttl := mr.TTL(anthropicSaturationKey(42))
	require.InDelta(t, float64(91*time.Second), float64(ttl), float64(2*time.Second))

	require.NoError(t, rdb.ZAdd(ctx, anthropicSaturationKey(42), redis.Z{Score: old, Member: "old-read"}).Err())
	got, err := cache.GetSaturationBatch(ctx, []int64{42}, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), got[42], "only the event younger than the rolling window remains")
}

func TestAnthropicSaturationCounter_GetBatch(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := cache.IncrementSaturation(ctx, 1, 90)
		require.NoError(t, err)
	}
	_, err := cache.IncrementSaturation(ctx, 2, 90)
	require.NoError(t, err)

	// id 3 never incremented => absent.
	out, err := cache.GetSaturationBatch(ctx, []int64{1, 2, 3}, 90)
	require.NoError(t, err)
	require.Equal(t, int64(5), out[1])
	require.Equal(t, int64(1), out[2])
	_, present := out[3]
	require.False(t, present, "absent keys must not appear in the batch result")

	// Empty input => empty map, no error.
	empty, err := cache.GetSaturationBatch(ctx, nil, 90)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestAnthropicSaturationCounter_InvalidWindow(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	_, err := cache.IncrementSaturation(context.Background(), 1, 0)
	require.Error(t, err)
	_, err = cache.GetSaturationBatch(context.Background(), []int64{1}, 0)
	require.Error(t, err)
}
