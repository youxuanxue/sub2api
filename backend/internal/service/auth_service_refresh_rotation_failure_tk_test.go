//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingGraceCache struct {
	*statefulRefreshCache
	failAt         string
	deleteFails    bool
	storedHashes   []string
	deleteAttempts int
}

func (c *failingGraceCache) StoreRefreshToken(ctx context.Context, hash string, data *RefreshTokenData, ttl time.Duration) error {
	c.storedHashes = append(c.storedHashes, hash)
	if c.failAt == "mark" && data.RotatedAt != nil {
		return errors.New("redis unavailable")
	}
	return c.statefulRefreshCache.StoreRefreshToken(ctx, hash, data, ttl)
}

func (c *failingGraceCache) DeleteRefreshToken(ctx context.Context, hash string) error {
	c.deleteAttempts++
	if c.deleteFails {
		return errors.New("redis unavailable")
	}
	return c.statefulRefreshCache.DeleteRefreshToken(ctx, hash)
}

func (c *failingGraceCache) AddToUserTokenSet(ctx context.Context, id int64, hash string, ttl time.Duration) error {
	if c.failAt == "user_index" {
		return errors.New("redis unavailable")
	}
	return c.statefulRefreshCache.AddToUserTokenSet(ctx, id, hash, ttl)
}

func (c *failingGraceCache) AddToFamilyTokenSet(ctx context.Context, family, hash string, ttl time.Duration) error {
	if c.failAt == "family_index" {
		return errors.New("redis unavailable")
	}
	return c.statefulRefreshCache.AddToFamilyTokenSet(ctx, family, hash, ttl)
}

func TestRefreshGraceCacheFailureDoesNotIssueTokens(t *testing.T) {
	for _, point := range []string{"mark", "user_index", "family_index"} {
		for _, deleteFails := range []bool{false, true} {
			t.Run(point+map[bool]string{false: "/cleanup_ok", true: "/cleanup_failed"}[deleteFails], func(t *testing.T) {
				cache := &failingGraceCache{statefulRefreshCache: newStatefulRefreshCache(), failAt: point, deleteFails: deleteFails}
				user := activeUser()
				raw := seedToken(cache.statefulRefreshCache, user)
				svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)
				pair, err := svc.RefreshTokenPair(context.Background(), raw)
				require.Error(t, err)
				require.Nil(t, pair)
				require.Equal(t, 1, cache.deleteAttempts)
				if point == "mark" {
					require.ErrorIs(t, err, ErrServiceUnavailable)
					require.Equal(t, []string{hashToken(raw)}, cache.storedHashes, "failed rotation must not attempt successor storage")
				} else {
					require.Len(t, cache.storedHashes, 2)
					if !deleteFails {
						_, exists := cache.store[cache.storedHashes[1]]
						require.False(t, exists, "failed indexing must remove the unpublished token")
					}
				}
			})
		}
	}
}
