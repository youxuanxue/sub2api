//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRefreshRotationRealRedisPublishesOnceAndRevokesReplay(t *testing.T) {
	ctx := context.Background()
	// Revocation constructs token keys inside Lua, so the generic test hook
	// cannot namespace its ARGV prefix. Isolate with a unique user/family instead.
	rdb := redis.NewClient(integrationRedis.Options())
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewRefreshTokenCache(rdb).(*refreshTokenCache)
	id := time.Now().UnixNano()
	data := &service.RefreshTokenData{UserID: id, FamilyID: t.Name(), TokenVersion: 1, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	t.Cleanup(func() { require.NoError(t, cache.DeleteUserRefreshTokens(context.Background(), id)) })
	require.NoError(t, cache.StoreRefreshToken(ctx, "old", data, time.Hour))
	require.NoError(t, cache.AddToUserTokenSet(ctx, data.UserID, "old", time.Hour))
	require.NoError(t, cache.AddToFamilyTokenSet(ctx, data.FamilyID, "old", time.Hour))
	_, owned, err := cache.ClaimRefreshToken(ctx, "old", "owner", 10*time.Second)
	require.NoError(t, err)
	require.True(t, owned)
	_, owned, err = cache.ClaimRefreshToken(ctx, "old", "other", 10*time.Second)
	require.NoError(t, err)
	require.False(t, owned)
	ok, err := cache.CompleteRefreshTokenRotation(ctx, "old", "other", "unexpected", data, "ciphertext-other", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = cache.CompleteRefreshTokenRotation(ctx, "old", "owner", "next", data, "ciphertext-one", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	got, owned, err := cache.ClaimRefreshToken(ctx, "old", "retry", 10*time.Second)
	require.NoError(t, err)
	require.False(t, owned)
	require.Equal(t, "ciphertext-one", got.RotationResult)
	require.NoError(t, cache.DeleteTokenFamily(ctx, data.FamilyID))
	_, err = cache.GetRefreshToken(ctx, "next")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
	_, err = cache.GetRefreshToken(ctx, "old")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
	ok, err = cache.CompleteRefreshTokenRotation(ctx, "old", "owner", "resurrected", data, "ciphertext-two", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}
