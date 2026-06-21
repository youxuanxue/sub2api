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
	zero, err := cache.GetSaturationBatch(ctx, []int64{7})
	require.NoError(t, err)
	require.Empty(t, zero, "absent key must not appear")

	// Three increments => count 3, batch read reflects it without mutating.
	for i := 1; i <= 3; i++ {
		c, incErr := cache.IncrementSaturation(ctx, 7, 90, 150)
		require.NoError(t, incErr)
		require.Equal(t, int64(i), c)
	}
	got, err := cache.GetSaturationBatch(ctx, []int64{7, 8})
	require.NoError(t, err)
	require.Equal(t, int64(3), got[7])
	_, has8 := got[8] // a different account is independent
	require.False(t, has8)
}

func TestAnthropicSaturationCounter_FixedWindowTTLSelfClears(t *testing.T) {
	cache, mr := newSaturationTestCache(t)
	ctx := context.Background()

	_, err := cache.IncrementSaturation(ctx, 42, 90, 150)
	require.NoError(t, err)
	// TTL is set on the first INCR (key was absent).
	ttl := mr.TTL(anthropicSaturationKey(42))
	require.InDelta(t, float64(90*time.Second), float64(ttl), float64(2*time.Second))

	// Subsequent increments within the window keep the ORIGINAL window (TTL is
	// only (re)set when the key was absent), so the window does not slide forever.
	mr.FastForward(30 * time.Second)
	_, err = cache.IncrementSaturation(ctx, 42, 90, 150)
	require.NoError(t, err)
	ttlAfter := mr.TTL(anthropicSaturationKey(42))
	require.LessOrEqual(t, ttlAfter, 60*time.Second, "window must not slide forward on later hits")

	// After the window fully elapses, the counter self-clears back to 0.
	mr.FastForward(90 * time.Second)
	got, err := cache.GetSaturationBatch(ctx, []int64{42})
	require.NoError(t, err)
	require.Empty(t, got, "counter must self-clear on TTL expiry")
}

func TestAnthropicSaturationCounter_GetBatch(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := cache.IncrementSaturation(ctx, 1, 90, 150)
		require.NoError(t, err)
	}
	_, err := cache.IncrementSaturation(ctx, 2, 90, 150)
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

func TestAnthropicSaturationCounter_InvalidArgs(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	_, err := cache.IncrementSaturation(context.Background(), 1, 0, 150)
	require.Error(t, err, "non-positive window rejected")
	_, err = cache.IncrementSaturation(context.Background(), 1, 90, 0)
	require.Error(t, err, "non-positive streak ttl rejected")
}

func TestAnthropicSaturationCounter_StreakFirstSeenNXLastSeenOverwrites(t *testing.T) {
	cache, mr := newSaturationTestCache(t)
	ctx := context.Background()

	// Single fresh hit: firstSeen == lastSeen (span 0), both = current Redis TIME.
	_, err := cache.IncrementSaturation(ctx, 5, 90, 150)
	require.NoError(t, err)
	got, err := cache.GetSaturationStreakBatch(ctx, []int64{5})
	require.NoError(t, err)
	require.Positive(t, got[5].FirstSeenUnix)
	require.Equal(t, got[5].FirstSeenUnix, got[5].LastSeenUnix, "single hit => span 0")

	// Seed a pre-existing streak that started long ago (miniredis TIME is real
	// wall-clock and not moved by FastForward, so seed explicit epochs). The next
	// hit must PRESERVE firstSeen (set-once / NX) and OVERWRITE lastSeen to now, so
	// the streak span reflects the real outage duration — the sustained signal.
	mr.Set(anthropicSaturationFirstKey(7), "1000")
	mr.Set(anthropicSaturationLastKey(7), "1000")
	_, err = cache.IncrementSaturation(ctx, 7, 90, 150)
	require.NoError(t, err)
	got, err = cache.GetSaturationStreakBatch(ctx, []int64{7})
	require.NoError(t, err)
	require.Equal(t, int64(1000), got[7].FirstSeenUnix, "firstSeen is set-once (NX), preserved across hits")
	require.Greater(t, got[7].LastSeenUnix, int64(1000), "lastSeen is overwritten to now each hit")
	require.Greater(t, got[7].LastSeenUnix-got[7].FirstSeenUnix, int64(120), "span grows with the streak (sustained)")
}

func TestAnthropicSaturationCounter_StreakSelfClearsAfterTTL(t *testing.T) {
	cache, mr := newSaturationTestCache(t)
	ctx := context.Background()

	const streakTTL = 150 // streak TTL passed below; service owns the canonical const
	_, err := cache.IncrementSaturation(ctx, 8, 90, streakTTL)
	require.NoError(t, err)
	got, err := cache.GetSaturationStreakBatch(ctx, []int64{8})
	require.NoError(t, err)
	require.Contains(t, got, int64(8), "streak present right after a hit")

	// No further hits for longer than the sliding streak TTL => keys expire and the
	// streak self-clears (edge recovered), so the stub is re-included.
	mr.FastForward((streakTTL + 5) * time.Second)
	got, err = cache.GetSaturationStreakBatch(ctx, []int64{8})
	require.NoError(t, err)
	require.Empty(t, got, "streak must self-clear after TTL of silence")
}

func TestAnthropicSaturationCounter_StreakBatchAbsentAndEmpty(t *testing.T) {
	cache, _ := newSaturationTestCache(t)
	ctx := context.Background()

	_, err := cache.IncrementSaturation(ctx, 1, 90, 150)
	require.NoError(t, err)
	out, err := cache.GetSaturationStreakBatch(ctx, []int64{1, 2})
	require.NoError(t, err)
	require.Contains(t, out, int64(1))
	_, has2 := out[2]
	require.False(t, has2, "account with no streak must be absent")

	empty, err := cache.GetSaturationStreakBatch(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
