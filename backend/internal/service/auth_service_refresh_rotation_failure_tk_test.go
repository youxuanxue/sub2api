//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type failingRotationCache struct {
	*statefulRefreshCache
	claimErr       error
	completeErr    error
	rejectComplete bool
}

func (c *failingRotationCache) ClaimRefreshToken(ctx context.Context, hash, claim string, grace time.Duration) (*RefreshTokenData, bool, error) {
	if c.claimErr != nil {
		return nil, false, c.claimErr
	}
	return c.statefulRefreshCache.ClaimRefreshToken(ctx, hash, claim, grace)
}

func (c *failingRotationCache) CompleteRefreshTokenRotation(ctx context.Context, hash, claim, nextHash string, next *RefreshTokenData, result string, ttl time.Duration) (bool, error) {
	if c.completeErr != nil || c.rejectComplete {
		return false, c.completeErr
	}
	return c.statefulRefreshCache.CompleteRefreshTokenRotation(ctx, hash, claim, nextHash, next, result, ttl)
}

func TestRefreshRotationFailsClosedAtClaimAndPublish(t *testing.T) {
	for _, point := range []string{"claim", "publish", "lost_claim"} {
		t.Run(point, func(t *testing.T) {
			cache := &failingRotationCache{statefulRefreshCache: newStatefulRefreshCache()}
			switch point {
			case "claim":
				cache.claimErr = errors.New("redis unavailable")
			case "publish":
				cache.completeErr = errors.New("redis unavailable")
			case "lost_claim":
				cache.rejectComplete = true
			}
			user := activeUser()
			raw := seedToken(cache.statefulRefreshCache, user)
			svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)
			pair, err := svc.RefreshTokenPair(context.Background(), raw)
			require.ErrorIs(t, err, ErrServiceUnavailable)
			require.Nil(t, pair)
			require.Len(t, cache.store, 1, "failure must not publish an unreturned successor")
			if point != "claim" {
				require.NotNil(t, cache.store[hashToken(raw)].RotatedAt)
			}
		})
	}
}

func TestRefreshRotationReplayCiphertextAndTamper(t *testing.T) {
	cache := newStatefulRefreshCache()
	user := activeUser()
	raw := seedToken(cache, user)
	svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)
	pair, err := svc.RefreshTokenPair(context.Background(), raw)
	require.NoError(t, err)
	sealed := cache.store[hashToken(raw)].RotationResult
	require.NotEmpty(t, sealed)
	require.False(t, strings.Contains(sealed, pair.AccessToken))
	require.False(t, strings.Contains(sealed, pair.RefreshToken))
	replayed, err := svc.RefreshTokenPair(context.Background(), raw)
	require.NoError(t, err)
	require.Equal(t, pair, replayed)
	cache.store[hashToken(raw)].RotationResult = "AAAA" + sealed[4:]
	replayed, err = svc.RefreshTokenPair(context.Background(), raw)
	require.ErrorIs(t, err, ErrServiceUnavailable)
	require.Nil(t, replayed)
	require.Len(t, cache.store, 2)
}

func TestRefreshRotationReplayRechecksUserAndGrace(t *testing.T) {
	for _, state := range []string{"disabled", "revoked", "future", "expired"} {
		t.Run(state, func(t *testing.T) {
			cache := newStatefulRefreshCache()
			user := activeUser()
			raw := seedToken(cache, user)
			svc := newTestAuthServiceForGrace(&userRepoStub{user: user}, cache)
			_, err := svc.RefreshTokenPair(context.Background(), raw)
			require.NoError(t, err)
			switch state {
			case "disabled":
				user.Status = "disabled"
			case "revoked":
				user.TokenVersion++
			case "future":
				at := time.Now().Add(time.Minute)
				cache.store[hashToken(raw)].RotatedAt = &at
			case "expired":
				at := time.Now().Add(-tkRefreshRotationGrace)
				cache.store[hashToken(raw)].RotatedAt = &at
			}
			pair, err := svc.RefreshTokenPair(context.Background(), raw)
			require.Error(t, err)
			require.Nil(t, pair)
		})
	}
}
