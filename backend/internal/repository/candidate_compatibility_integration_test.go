//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// These key formats are the v1.8.204 storage boundary, deliberately independent
// of today's key builders. This is a persistent-format upgrade/rollback test,
// not a claim that a full old server binary was deployed.
func TestCandidatePersistentFormatUpgradeAndRollback(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	legacyKey := func(prefix, response string) string {
		return fmt.Sprintf("sticky_session:1:%s%x", prefix, sha256.Sum256([]byte(response)))
	}
	const response = "resp_before_upgrade"
	for prefix, value := range map[string]int64{
		"openai:response:":                 115,
		"openai:http-response-owner:user:": 10,
		"openai:http-response-owner:key:":  20,
	} {
		require.NoError(t, rdb.Set(ctx, legacyKey(prefix, response), value, time.Minute).Err())
	}
	current := service.NewOpenAIWSStateStore(NewGatewayCache(rdb))
	ownerCtx := service.WithCandidateIdentity(ctx, 10, 21)
	account, err := current.GetResponseAccount(ownerCtx, 1, response)
	require.NoError(t, err)
	require.Equal(t, int64(115), account)
	user, key, found, err := current.GetHTTPResponseOwner(ownerCtx, 1, response)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(10), user)
	require.Equal(t, int64(20), key)

	// A new process has no local cache; newly written records retain old-reader data.
	require.NoError(t, current.BindResponseAccount(ownerCtx, 1, "resp_after_upgrade", 124, time.Minute))
	for prefix, want := range map[string]int64{
		"openai:response:":                 124,
		"openai:http-response-owner:user:": 10,
		"openai:http-response-owner:key:":  21,
	} {
		got, err := rdb.Get(ctx, legacyKey(prefix, "resp_after_upgrade")).Int64()
		require.NoError(t, err)
		require.Equal(t, want, got, "v1.8.204 reader must recover the same record")
	}
	restarted := service.NewOpenAIWSStateStore(NewGatewayCache(rdb))
	account, err = restarted.GetResponseAccount(ownerCtx, 11, "resp_after_upgrade")
	require.NoError(t, err)
	require.Equal(t, int64(124), account, "new reader survives billing-origin change and restart")

	// Soft affinity cold-starts once. Submitted-media routing is not soft affinity.
	cache := NewGatewayCache(rdb)
	require.NoError(t, rdb.Set(ctx, "sticky_session:1:ordinary-session", 115, time.Minute).Err())
	_, err = cache.GetSessionAccountID(ownerCtx, 1, "ordinary-session")
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	require.NoError(t, cache.SetSessionAccountID(ownerCtx, 1, "ordinary-session", 124, time.Minute))
	account, err = NewGatewayCache(rdb).GetSessionAccountID(ownerCtx, 11, "ordinary-session")
	require.NoError(t, err)
	require.Equal(t, int64(124), account)
	const media = "openai:grok-video:submitted-task-owner-hash"
	require.NoError(t, rdb.Set(ctx, "sticky_session:1:"+media, 65, time.Minute).Err())
	account, err = cache.GetSessionAccountID(ownerCtx, 1, media)
	require.NoError(t, err)
	require.Equal(t, int64(65), account)
	require.NoError(t, cache.SetSessionAccountID(ownerCtx, 1, media, 66, time.Minute))
	account, err = rdb.Get(ctx, "sticky_session:1:"+media).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(66), account)
}
