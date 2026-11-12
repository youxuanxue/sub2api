//go:build unit

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestRefreshRotationSingleOwnerAndAtomicPublication(t *testing.T) {
	cache := newRefreshTokenCacheForTest(t)
	ctx := context.Background()
	data := &service.RefreshTokenData{UserID: 7, FamilyID: "f", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, cache.StoreRefreshToken(ctx, "old", data, time.Hour))
	require.NoError(t, cache.AddToUserTokenSet(ctx, 7, "old", time.Hour))
	require.NoError(t, cache.AddToFamilyTokenSet(ctx, "f", "old", time.Hour))
	var wg sync.WaitGroup
	owners := make(chan string, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claim := fmt.Sprint(i)
			_, owned, err := cache.ClaimRefreshToken(ctx, "old", claim, 10*time.Second)
			if err != nil {
				t.Error(err)
			}
			if owned {
				owners <- claim
			}
		}(i)
	}
	wg.Wait()
	close(owners)
	claims := []string{}
	for claim := range owners {
		claims = append(claims, claim)
	}
	require.Len(t, claims, 1)
	ok, err := cache.CompleteRefreshTokenRotation(ctx, "old", "wrong-owner", "new", data, "encrypted-result", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = cache.CompleteRefreshTokenRotation(ctx, "old", claims[0], "new", data, "encrypted-result", time.Hour)
	require.NoError(t, err)
	require.True(t, ok)
	stored, owned, err := cache.ClaimRefreshToken(ctx, "old", "retry", 10*time.Second)
	require.NoError(t, err)
	require.False(t, owned)
	require.Equal(t, "encrypted-result", stored.RotationResult)
	require.NoError(t, cache.DeleteTokenFamily(ctx, "f"))
	_, err = cache.GetRefreshToken(ctx, "old")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
	_, err = cache.GetRefreshToken(ctx, "new")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
	ok, err = cache.CompleteRefreshTokenRotation(ctx, "old", claims[0], "resurrected", data, "encrypted-result", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestRefreshRotationRevocationBeforePublicationFailsClosed(t *testing.T) {
	cache := newRefreshTokenCacheForTest(t)
	ctx := context.Background()
	data := &service.RefreshTokenData{UserID: 7, FamilyID: "f", ExpiresAt: time.Now().Add(time.Hour)}
	require.NoError(t, cache.StoreRefreshToken(ctx, "old", data, time.Hour))
	require.NoError(t, cache.AddToUserTokenSet(ctx, 7, "old", time.Hour))
	require.NoError(t, cache.AddToFamilyTokenSet(ctx, "f", "old", time.Hour))
	_, owned, err := cache.ClaimRefreshToken(ctx, "old", "claim", 10*time.Second)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, cache.DeleteUserRefreshTokens(ctx, 7))
	ok, err := cache.CompleteRefreshTokenRotation(ctx, "old", "claim", "new", data, "encrypted-result", time.Hour)
	require.NoError(t, err)
	require.False(t, ok)
	_, err = cache.GetRefreshToken(ctx, "new")
	require.ErrorIs(t, err, service.ErrRefreshTokenNotFound)
}
