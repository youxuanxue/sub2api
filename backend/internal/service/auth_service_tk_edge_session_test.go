//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

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

	pair, err := s.GenerateEdgeAdminSessionTokenPair(context.Background(), user)
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

	pair, err := s.GenerateEdgeAdminSessionTokenPair(context.Background(), user)
	require.Error(t, err)
	require.Nil(t, pair)
}
