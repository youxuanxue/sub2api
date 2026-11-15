package repository

import (
	"context"
	"testing"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestKiroCacheFingerprintStoreBatchReadWriteAndTTLRetention(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := &kiroCacheFingerprintStore{rdb: rdb}
	ctx := context.Background()
	session := "session"
	entries := []kiroproto.CacheFingerprintEntry{
		{FingerprintHex: "first", Tokens: 1200},
		{FingerprintHex: "second", Tokens: 2400},
		{FingerprintHex: "", Tokens: 99},
		{FingerprintHex: "ignored", Tokens: 0},
	}

	require.NoError(t, store.PutMany(ctx, session, entries, time.Minute))
	got, err := store.GetMany(ctx, session, []string{"first", "missing", "second"})
	require.NoError(t, err)
	require.Equal(t, []int{1200, 0, 2400}, got)

	mr.FastForward(20 * time.Second)
	require.NoError(t, store.PutMany(ctx, session, []kiroproto.CacheFingerprintEntry{
		{FingerprintHex: "first", Tokens: 1300},
	}, 10*time.Second))
	remaining, err := rdb.TTL(ctx, store.key(session, "first")).Result()
	require.NoError(t, err)
	require.Greater(t, remaining, 10*time.Second)
	got, err = store.GetMany(ctx, session, []string{"first"})
	require.NoError(t, err)
	require.Equal(t, []int{1300}, got)
}
