package service

// TK-only: short-lived admin session token for the prod→edge "manage accounts"
// handoff (see handler/edge_tk_admin_session_handler.go and the prod overview's
// "去管理账号" jump). Kept in a companion file so auth_service.go stays close to
// upstream shape (CLAUDE.md §5).
//
// Why a dedicated minter instead of GenerateToken: GenerateToken uses the
// deployment's configured access-token TTL (often an hour). A cross-deployment
// handoff token rides in a URL fragment, so it must be SHORT-LIVED (minutes) and
// independent of the login TTL. This mints the same HS256 claims shape as
// GenerateToken (so the edge's normal jwt_auth middleware validates it verbatim)
// but with an explicit, clamped TTL.

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// edgeAdminSessionMinTTL / MaxTTL clamp the handoff token lifetime. Short by
	// design: the token is a one-time bridge that the edge SPA consumes on load
	// and immediately scrubs from the URL. Long enough to survive a slow page
	// load + profile fetch, short enough that a leaked fragment expires fast.
	edgeAdminSessionMinTTL = 1 * time.Minute
	edgeAdminSessionMaxTTL = 10 * time.Minute
)

// GenerateEdgeAdminSessionToken mints a short-lived admin JWT for the edge
// handoff. ttl is clamped to [edgeAdminSessionMinTTL, edgeAdminSessionMaxTTL].
// The caller MUST have already verified user.IsAdmin() — this only signs.
func (s *AuthService) GenerateEdgeAdminSessionToken(user *User, ttl time.Duration) (string, error) {
	if user == nil {
		return "", fmt.Errorf("nil user")
	}
	if ttl < edgeAdminSessionMinTTL {
		ttl = edgeAdminSessionMinTTL
	}
	if ttl > edgeAdminSessionMaxTTL {
		ttl = edgeAdminSessionMaxTTL
	}
	now := time.Now()
	claims := &JWTClaims{
		UserID:       user.ID,
		Email:        user.Email,
		Role:         user.Role,
		TokenVersion: resolvedTokenVersion(user),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(s.cfg.JWT.Secret))
	if err != nil {
		return "", fmt.Errorf("sign edge admin session token: %w", err)
	}
	return tokenString, nil
}

// EdgeAdminSessionTTL is the default handoff token lifetime, exported so the edge
// handler returns a matching expires_in to the caller.
const EdgeAdminSessionTTL = 5 * time.Minute
