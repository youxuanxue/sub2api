//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// newEdgeSessionAuthService builds an AuthService wired with the in-package
// refreshTokenCacheStub so GenerateEdgeAdminSessionTokenPair can mint a renewable
// pair (access + refresh) without Redis.
func newEdgeSessionAuthService(cache RefreshTokenCache) *AuthService {
	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:                   "edge-session-test-secret",
			AccessTokenExpireMinutes: 30,
			RefreshTokenExpireDays:   7,
		},
	}
	return NewAuthService(
		nil, &userRepoStub{}, nil, cache, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
}

func TestGenerateEdgeAdminSessionTokenPair_ReturnsRenewablePair(t *testing.T) {
	s := newEdgeSessionAuthService(&refreshTokenCacheStub{})
	user := &User{ID: 1, Email: "admin@edge", Role: RoleAdmin, Status: StatusActive}

	pair, err := s.GenerateEdgeAdminSessionTokenPair(context.Background(), user, "edge-handoff-test")
	require.NoError(t, err)
	require.NotNil(t, pair)
	require.NotEmpty(t, pair.AccessToken, "access token establishes the session")
	require.True(t, strings.HasPrefix(pair.RefreshToken, refreshTokenPrefix),
		"refresh token is what lets the edge SPA self-renew the session")
	require.Equal(t, 30*60, pair.ExpiresIn, "expires_in drives the SPA's proactive refresh schedule")
}

func TestGenerateEdgeAdminSessionTokenPair_RequiresRefreshCache(t *testing.T) {
	// No refresh cache configured -> GenerateTokenPair errors; the handler maps
	// this to a 500 so prod surfaces it as a 502 rather than handing out a
	// non-renewing session silently.
	s := newEdgeSessionAuthService(nil)
	user := &User{ID: 1, Role: RoleAdmin, Status: StatusActive}

	pair, err := s.GenerateEdgeAdminSessionTokenPair(context.Background(), user, "edge-handoff-test")
	require.Error(t, err)
	require.Nil(t, pair)
}

type unindexedEdgeRefreshCache struct{ *statefulRefreshCache }

func (c *unindexedEdgeRefreshCache) AddToFamilyTokenSet(context.Context, string, string, time.Duration) error {
	return errors.New("index unavailable")
}
func TestGenerateEdgeAdminSessionTokenPair_FailsClosedWithoutFamily(t *testing.T) {
	cache := &unindexedEdgeRefreshCache{newStatefulRefreshCache()}
	s := newEdgeSessionAuthService(cache)
	pair, err := s.GenerateEdgeAdminSessionTokenPair(context.Background(), &User{ID: 1, Role: RoleAdmin, Status: StatusActive}, "edge-handoff-test")
	require.Error(t, err)
	require.Nil(t, pair)
	require.Empty(t, cache.store, "undelivered refresh token must be removed")
}
func TestGenerateEdgeAdminSessionTokenPair_RejectsInactiveOrNonAdmin(t *testing.T) {
	for _, user := range []*User{nil, {ID: 1, Role: RoleUser, Status: StatusActive}, {ID: 1, Role: RoleAdmin, Status: StatusDisabled}} {
		pair, err := newEdgeSessionAuthService(&refreshTokenCacheStub{}).GenerateEdgeAdminSessionTokenPair(context.Background(), user, "edge-handoff-test")
		require.Error(t, err)
		require.Nil(t, pair)
	}
}

func TestEdgeAdminHandoffRotatedFamilyAlsoFailsClosed(t *testing.T) {
	cache := &unindexedEdgeRefreshCache{newStatefulRefreshCache()}
	s := newEdgeSessionAuthService(cache)
	pair, err := s.GenerateTokenPair(context.Background(), &User{ID: 1, Role: RoleAdmin, Status: StatusActive}, "edge-handoff-rotated")
	require.Error(t, err)
	require.Nil(t, pair)
	require.Empty(t, cache.store)
}
