//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEdgeAdminHandoffRedis(t *testing.T) {
	rdb := testRedis(t)
	cache := NewEdgeAdminHandoffCache(rdb)
	ctx := context.Background()
	claims := service.EdgeHandoffClaims{Attempt: "attempt", Challenge: "challenge", ExpiresAt: time.Now().Add(time.Minute).Unix()}
	require.NoError(t, cache.Create(ctx, "attempt", "code", claims, time.Minute))
	require.Error(t, cache.Create(ctx, "attempt", "another", claims, time.Minute))
	_, err := cache.Consume(ctx, "code", "wrong", "challenge")
	require.Error(t, err)
	_, err = cache.Consume(ctx, "code", "attempt", "wrong")
	require.Error(t, err)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.Consume(ctx, "code", "attempt", "challenge"); err == nil {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int32(1), winners.Load())
	require.Error(t, cache.Create(ctx, "attempt", "another", claims, time.Minute), "consuming code does not reopen signed attempt")
	require.NoError(t, cache.Create(ctx, "expiring", "expiring", claims, time.Millisecond))
	require.Eventually(t, func() bool {
		n, err := rdb.Exists(ctx, "{edge-handoff}:code:expiring").Result()
		return err == nil && n == 0
	}, time.Second, time.Millisecond)
	_, err = cache.Consume(ctx, "expiring", "attempt", "challenge")
	require.Error(t, err)

	// Verify Redis outage fails closed on both creation and exchange.
	require.NoError(t, rdb.Close())
	require.Error(t, cache.Create(ctx, "outage", "outage", claims, time.Minute))
	_, err = cache.Consume(ctx, "code", "attempt", "challenge")
	require.Error(t, err)
}

type edgeHandoffUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r *edgeHandoffUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, nil
}
func TestEdgeAdminHandoffRefreshFamilyRevocation(t *testing.T) {
	rdb := testRedis(t)
	cache := NewRefreshTokenCache(rdb)
	ctx := context.Background()
	user := &service.User{ID: 9, Role: service.RoleAdmin, Status: service.StatusActive}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "local-isolated-edge-family-test", AccessTokenExpireMinutes: 30, RefreshTokenExpireDays: 7}}
	auth := service.NewAuthService(nil, &edgeHandoffUserRepo{user: user}, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	pair, err := auth.GenerateEdgeAdminSessionTokenPair(ctx, user, "edge-handoff-target")
	require.NoError(t, err)
	other, err := auth.GenerateTokenPair(ctx, user, "unrelated-login")
	require.NoError(t, err)
	rotated, err := auth.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(rotated.RefreshToken))
	data, err := cache.GetRefreshToken(ctx, hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	require.Equal(t, "edge-handoff-target", data.FamilyID)
	require.NoError(t, cache.DeleteTokenFamily(ctx, "edge-handoff-target"))
	_, err = auth.RefreshTokenPair(ctx, rotated.RefreshToken)
	require.Error(t, err)
	_, err = auth.RefreshTokenPair(ctx, other.RefreshToken)
	require.NoError(t, err, "targeted revocation preserves unrelated login")
}
