package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayCache_KiroSessionRecoveryClearsStickyAndConsumesOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	cache := NewGatewayCache(rdb)
	store, ok := cache.(service.KiroSessionRecoveryStore)
	require.True(t, ok)
	groupID := int64(7)
	const sessionHash = "session-a"

	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, sessionHash, 99, time.Hour))
	require.NoError(t, store.SetKiroSessionRecoveryExclusion(ctx, groupID, sessionHash, 99, time.Hour))
	_, err := cache.GetSessionAccountID(ctx, groupID, sessionHash)
	require.True(t, errors.Is(err, redis.Nil), "the failed sticky binding must be removed")

	ttl, err := rdb.TTL(ctx, buildKiroSessionRecoveryKey(groupID, sessionHash)).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 59*time.Minute)

	accountID, err := store.ConsumeKiroSessionRecoveryExclusion(ctx, groupID, sessionHash)
	require.NoError(t, err)
	require.Equal(t, int64(99), accountID)
	accountID, err = store.ConsumeKiroSessionRecoveryExclusion(ctx, groupID, sessionHash)
	require.NoError(t, err)
	require.Zero(t, accountID)
}
