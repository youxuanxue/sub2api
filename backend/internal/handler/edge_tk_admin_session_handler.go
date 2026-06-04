package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// EdgeAdminSessionHandler serves the TokenKey edge "admin session handoff" mint:
//
//	POST /api/v1/edge/admin-session
//
// It is the write-companion that turns the read-only cross-edge overview into a
// jump-and-manage console: prod's admin holds this edge's mirror-stub api-key and
// POSTs here to mint a RENEWABLE admin session (access + refresh), then opens the
// edge's own /admin/accounts page already logged in (see frontend EdgeHandoffView).
// The edge's account create/edit/delete + OAuth flows then run natively against
// THIS edge's own DB/Redis/egress — prod re-implements nothing.
//
// SECURITY POSTURE — this endpoint deliberately ELEVATES the edge api-key group
// (which was read-only/side-effect-free for /scheduling-capacity and /accounts)
// into a credential that can obtain an admin session. The irreducible guard:
//   - the presented api-key's OWNER must be an admin user on this edge
//     (user.IsAdmin()); a plain relay key cannot mint a session.
//
// The mint issues the SAME token pair a normal login does (access + refresh, via
// AuthService.GenerateEdgeAdminSessionTokenPair) so the edge session self-renews
// and the operator is not bounced to login mid-task. The refresh token rides in
// the handoff URL fragment (never sent to the server) and is scrubbed on load —
// same exposure as the OAuth login callbacks.
//
// Mounted behind the same lightweight edge api-key middleware as the reads, so
// the mirror-stub key prod already holds is the only secret (zero new secret).
type EdgeAdminSessionHandler struct {
	apiKeys apiKeyByKeyLookup
	users   userByIDLookup
	minter  edgeAdminSessionMinter
}

// apiKeyByKeyLookup resolves a presented api-key to its record (carrying the
// owner UserID). *service.APIKeyService satisfies it.
type apiKeyByKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

// userByIDLookup loads the api-key owner so the handler can check IsAdmin.
// *service.UserService satisfies it.
type userByIDLookup interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

// edgeAdminSessionMinter mints the renewable admin session (access + refresh).
// *service.AuthService satisfies it via GenerateEdgeAdminSessionTokenPair.
type edgeAdminSessionMinter interface {
	GenerateEdgeAdminSessionTokenPair(ctx context.Context, user *service.User) (*service.TokenPair, error)
}

// NewEdgeAdminSessionHandler wires the edge admin-session mint handler.
func NewEdgeAdminSessionHandler(apiKeys apiKeyByKeyLookup, users userByIDLookup, minter edgeAdminSessionMinter) *EdgeAdminSessionHandler {
	return &EdgeAdminSessionHandler{apiKeys: apiKeys, users: users, minter: minter}
}

// edgeAdminSessionResponse is the mint result returned to prod's forwarder.
// Token is the access JWT; RefreshToken lets the edge SPA self-renew the session;
// ExpiresIn is the access token lifetime in seconds.
type edgeAdminSessionResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Mint handles POST /api/v1/edge/admin-session. The api-key was already validated
// as active by the edge auth middleware; here we additionally require its owner
// to be an admin before minting.
func (h *EdgeAdminSessionHandler) Mint(c *gin.Context) {
	if h == nil || h.apiKeys == nil || h.users == nil || h.minter == nil {
		response.Error(c, http.StatusInternalServerError, "edge admin session handler unavailable")
		return
	}

	key := extractEdgeAdminSessionKey(c)
	if key == "" {
		response.Error(c, http.StatusUnauthorized, "api key required")
		return
	}

	ctx := c.Request.Context()
	apiKey, err := h.apiKeys.GetByKey(ctx, key)
	if err != nil || apiKey == nil {
		response.Error(c, http.StatusUnauthorized, "invalid api key")
		return
	}

	user, err := h.users.GetByID(ctx, apiKey.UserID)
	if err != nil || user == nil {
		response.Error(c, http.StatusUnauthorized, "api key owner not found")
		return
	}
	// The elevation gate: only an admin-owned key may mint an admin session.
	if !user.IsAdmin() || !user.IsActive() {
		response.Error(c, http.StatusForbidden, "admin api key required")
		return
	}

	pair, err := h.minter.GenerateEdgeAdminSessionTokenPair(ctx, user)
	if err != nil || pair == nil {
		response.Error(c, http.StatusInternalServerError, "failed to mint admin session")
		return
	}

	response.Success(c, edgeAdminSessionResponse{
		Token:        pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
	})
}

// extractEdgeAdminSessionKey mirrors the edge auth middleware's extraction order
// (Authorization: Bearer, then x-api-key) so the handler reads the same key the
// middleware already validated — the middleware sets no context identity.
func extractEdgeAdminSessionKey(c *gin.Context) string {
	if authHeader := c.GetHeader("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			if k := strings.TrimSpace(parts[1]); k != "" {
				return k
			}
		}
	}
	return strings.TrimSpace(c.GetHeader("x-api-key"))
}
