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

func TestAntigravitySaturationCounterCache_RollingWindow(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewAntigravitySaturationCounterCache(rdb)
	ctx := context.Background()

	count, err := cache.IncrementSaturation(ctx, 85, "gemini-3-flash-tiered", 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.InDelta(t, float64(91*time.Second), mr.TTL(antigravitySaturationKey(85, "gemini-3-flash-tiered")), float64(2*time.Second))

	otherCount, err := cache.IncrementSaturation(ctx, 85, "claude-sonnet-4-6", 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), otherCount)
	require.InDelta(t, float64(91*time.Second), mr.TTL(antigravitySaturationKey(85, "claude-sonnet-4-6")), float64(2*time.Second))

	scopes := []service.AntigravitySaturationScope{{AccountID: 85, ModelKey: "gemini-3-flash-tiered"}, {AccountID: 85, ModelKey: "claude-sonnet-4-6"}, {AccountID: 86, ModelKey: "gemini-3-flash-tiered"}}
	reader := cache.(service.AntigravitySaturationReader)
	counts, err := reader.GetSaturationBatch(ctx, scopes, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts[scopes[0]])
	require.Equal(t, int64(1), counts[scopes[1]])
	require.Zero(t, counts[scopes[2]])
	old := float64(time.Now().Add(-91 * time.Second).UnixMilli())
	require.NoError(t, rdb.ZAdd(ctx, antigravitySaturationKey(85, "gemini-3-flash-tiered"), redis.Z{Score: old, Member: "old-read"}).Err())
	counts, err = reader.GetSaturationBatch(ctx, scopes, 90)
	require.NoError(t, err)
	require.Equal(t, int64(1), counts[scopes[0]], "the expired event must not count")
	require.Equal(t, int64(1), counts[scopes[1]], "independent model scope keeps its window")
}
