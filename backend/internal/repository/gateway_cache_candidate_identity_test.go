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

func TestGatewayCache_CandidateAffinityFollowsAccountAcrossBillingOrigins(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	ctx := service.WithCandidateIdentity(context.Background(), 10, 20)
	require.NoError(t, cache.SetSessionAccountID(ctx, 1, "openai:session", 115, time.Hour))
	accountID, err := cache.GetSessionAccountID(ctx, 11, "openai:session")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
	require.Equal(t, []string{"sticky_session:0:candidate:v1:user:10:key:20:openai:session"}, server.Keys())
	for _, other := range []context.Context{
		service.WithCandidateIdentity(context.Background(), 99, 20),
		service.WithCandidateIdentity(context.Background(), 10, 21),
	} {
		_, err := cache.GetSessionAccountID(other, 1, "openai:session")
		require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	}
	require.NoError(t, cache.RefreshSessionTTL(ctx, 11, "openai:session", 2*time.Hour))
	server.FastForward(time.Hour)
	_, err = cache.GetSessionAccountID(ctx, 1, "openai:session")
	require.NoError(t, err)
	require.NoError(t, cache.DeleteSessionAccountID(ctx, 11, "openai:session"))
	_, err = cache.GetSessionAccountID(ctx, 1, "openai:session")
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
}

func TestGatewayCache_CandidateKiroRecoveryUsesSameAffinityNamespace(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	recovery, ok := cache.(service.KiroSessionRecoveryStore)
	require.True(t, ok)
	ctx := service.WithCandidateIdentity(context.Background(), 10, 20)
	require.NoError(t, cache.SetSessionAccountID(ctx, 1, "session", 115, time.Hour))
	require.NoError(t, recovery.SetKiroSessionRecoveryExclusion(ctx, 11, "session", 115, time.Hour))
	_, err := cache.GetSessionAccountID(ctx, 1, "session")
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	accountID, err := recovery.ConsumeKiroSessionRecoveryExclusion(ctx, 1, "session")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
}

func TestGatewayCache_CandidateIdentityPreservesSubmittedMediaBindings(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	ctx := service.WithCandidateIdentity(context.Background(), 10, 20)
	const mediaKey = "openai:grok-video:submitted-task-owner-hash"
	require.NoError(t, cache.SetSessionAccountID(context.Background(), 1, mediaKey, 65, time.Hour))
	accountID, err := cache.GetSessionAccountID(ctx, 1, mediaKey)
	require.NoError(t, err)
	require.Equal(t, int64(65), accountID)
	require.NoError(t, cache.SetSessionAccountID(ctx, 1, mediaKey, 66, time.Hour))
	accountID, err = cache.GetSessionAccountID(context.Background(), 1, mediaKey)
	require.NoError(t, err)
	require.Equal(t, int64(66), accountID)
}

func TestGatewayCache_CandidateHardStatePersistsAndExpiresWithoutExtendingOwnership(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	writer := service.NewOpenAIWSStateStore(cache)
	ctx := service.WithCandidateIdentity(context.Background(), 10, 20)
	require.NoError(t, writer.BindResponseAccount(ctx, 1, "resp_owned", 115, time.Minute))

	reader := service.NewOpenAIWSStateStore(cache)
	newKeyCtx := service.WithCandidateIdentity(context.Background(), 10, 21)
	accountID, err := reader.GetResponseAccount(newKeyCtx, 11, "resp_owned")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
	userID, keyID, found, err := reader.GetHTTPResponseOwner(newKeyCtx, 11, "resp_owned")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(10), userID)
	require.Equal(t, int64(20), keyID)

	rollback := service.NewOpenAIWSStateStore(cache)
	accountID, err = rollback.GetResponseAccount(context.Background(), 1, "resp_owned")
	require.NoError(t, err)
	require.Equal(t, int64(115), accountID)
	_, _, found, err = rollback.GetHTTPResponseOwner(context.Background(), 1, "resp_owned")
	require.NoError(t, err)
	require.True(t, found)

	server.FastForward(2 * time.Minute)
	accountID, err = reader.GetResponseAccount(newKeyCtx, 11, "resp_owned")
	require.NoError(t, err)
	require.Zero(t, accountID)
	_, _, found, err = reader.GetHTTPResponseOwner(newKeyCtx, 11, "resp_owned")
	require.NoError(t, err)
	require.False(t, found, "reading a Redis owner must not extend its lifetime in local cache")
}

func TestGatewayCache_CandidateHardStatePreservesInfrastructureErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := service.NewOpenAIWSStateStore(NewGatewayCache(client))
	ctx := service.WithCandidateIdentity(context.Background(), 10, 20)
	server.SetError("ERR unavailable")
	_, err := store.GetResponseAccount(ctx, 1, "resp_unknown")
	require.ErrorContains(t, err, "unavailable")
	_, _, _, err = store.GetHTTPResponseOwner(ctx, 1, "resp_unknown")
	require.ErrorContains(t, err, "unavailable")
}
