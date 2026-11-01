//go:build unit

package openai

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func testOAuthSession(state string) *OAuthSession {
	return &OAuthSession{
		State:        state,
		CodeVerifier: "verifier-" + state,
		ClientID:     ClientID,
		RedirectURI:  DefaultRedirectURI,
		CreatedAt:    time.Now(),
	}
}

func TestRedisSessionStoreSharesSessionsAndSingleUseAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	writer := NewRedisSessionStore(client)
	reader := NewRedisSessionStore(client)
	t.Cleanup(writer.Stop)
	t.Cleanup(reader.Stop)

	writer.Set("sid", testOAuthSession("state"))
	got, ok := reader.Get("sid")
	require.True(t, ok)
	require.Equal(t, "state", got.State)
	require.Equal(t, "verifier-state", got.CodeVerifier)

	require.True(t, reader.TryConsumeSession("sid"))
	require.False(t, writer.TryConsumeSession("sid"))
	reader.Delete("sid")
	_, ok = writer.Get("sid")
	require.False(t, ok)
}

func TestRedisSessionStoreFallsBackToLocalOnlyWhenWriteFails(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisSessionStore(client)
	t.Cleanup(store.Stop)

	mr.Close()
	store.Set("local-only", testOAuthSession("local"))

	got, ok := store.Get("local-only")
	require.True(t, ok)
	require.Equal(t, "local", got.State)
	require.True(t, store.TryConsumeSession("local-only"))
	require.False(t, store.TryConsumeSession("local-only"))
}

func TestRedisSessionStoreDoesNotReviveStaleLocalCopyAfterRemoteMiss(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisSessionStore(client)
	t.Cleanup(store.Stop)

	store.Set("remote", testOAuthSession("remote"))
	require.NoError(t, store.remote.Delete(context.Background(), "remote"))
	_, ok := store.Get("remote")
	require.False(t, ok)
}

func TestRedisSessionStoreHonorsTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisSessionStore(client)
	t.Cleanup(store.Stop)

	store.Set("sid", testOAuthSession("state"))
	mr.FastForward(SessionTTL + time.Second)
	_, ok := store.Get("sid")
	require.False(t, ok)
	require.False(t, store.TryConsumeSession("sid"))
}
