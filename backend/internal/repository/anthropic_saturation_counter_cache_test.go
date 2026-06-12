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

	// Empty key reads 0.
	n, err := cache.GetSaturation(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, int64(0), n)

	// Three increments => count 3, Get reflects it without mutating.
	for i := 1; i <= 3; i++ {
		c, incErr := cache.IncrementSaturation(ctx, 7, 90)
		require.NoError(t, incErr)
		require.Equal(t, int64(i), c)
	}
	got, err := cache.GetSaturation(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, int64(3), got)

	// A different account is independent.
	other, err := cache.GetSaturation(ctx, 8)
	require.NoError(t, err)
	require.Equal(t, int64(0), other)
}

func TestAnthropicSaturationCounter_FixedWindowTTLSelfClears(t *testing.T) {
	cache, mr := newSaturationTestCache(t)
	ctx := context.Background()

	_, err := cache.IncrementSaturation(ctx, 42, 90)
	require.NoError(t, err)
	// TTL is set on the first INCR (key was absent).
	ttl := mr.TTL(anthropicSaturationKey(42))
	require.InDelta(t, float64(90*time.Second), float64(ttl), float64(2*time.Second))

	// Subsequent increments within the window keep the ORIGINAL window (TTL is
	// only (re)set when the key was absent), so the window does not slide forever.
	mr.FastForward(30 * time.Second)
	_, err = cache.IncrementSaturation(ctx, 42, 90)
	require.NoError(t, err)
	ttlAfter := mr.TTL(anthropicSaturationKey(42))
	require.LessOrEqual(t, ttlAfter, 60*time.Second, "window must not slide forward on later hits")

	// After the window fully elapses, the counter self-clears back to 0.
	mr.FastForward(90 * time.Second)
	got, err := cache.GetSaturation(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, int64(0), got, "counter must self-clear on TTL expiry")
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
	out, err := cache.GetSaturationBatch(ctx, []int64{1, 2, 3})
	require.NoError(t, err)
	require.Equal(t, int64(5), out[1])
	require.Equal(t, int64(1), out[2])
	_, present := out[3]
	require.False(t, present, "absent keys must not appear in the batch result")

	// Empty input => empty map, no error.
	empty, err := cache.GetSaturationBatch(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestAnthropicSaturationCounter_InvalidWindow(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	_, err := cache.IncrementSaturation(context.Background(), 1, 0)
	require.Error(t, err)
}
