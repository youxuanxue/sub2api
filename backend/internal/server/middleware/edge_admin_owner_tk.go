package middleware

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// edgeAdminOwnerKeyLookup resolves a presented api-key to its record (carrying
// the owner UserID). *service.APIKeyService satisfies it.
type edgeAdminOwnerKeyLookup interface {
	GetByKey(ctx context.Context, key string) (*service.APIKey, error)
}

// edgeAdminOwnerUserLookup loads the api-key owner so the gate can check admin
// status. *service.UserService satisfies it.
type edgeAdminOwnerUserLookup interface {
	GetByID(ctx context.Context, id int64) (*service.User, error)
}

// NewEdgeAdminOwnerMiddleware ELEVATES the lightweight edge api-key check: in
// addition to the key resolving to a usable key (NewEdgeCapacityAuthMiddleware,
// which MUST run first for the active-status check), the key's OWNER must be an
// admin user on this deployment.
//
// It is the reusable form of the admin-ownership gate inlined in the edge
// admin-session mint handler (handler/edge_tk_admin_session_handler.go) — same
// irreducible guard (user.IsAdmin() && user.IsActive()), here layered onto the
// edge account WRITE ops (POST/DELETE /api/v1/edge/accounts/:id/<op>) so a plain
// relay key can READ the credential-free inventory but only an admin-owned key
// may MUTATE an account. The same gate is what lets §P3 optionally tighten the
// read endpoints without a second auth mechanism.
//
// It re-extracts the key via extractEdgeCapacityKey (the capacity middleware sets
// no context identity), mirroring the admin-session handler which re-reads the
// key the middleware already validated.
func NewEdgeAdminOwnerMiddleware(keys edgeAdminOwnerKeyLookup, users edgeAdminOwnerUserLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		if keys == nil || users == nil {
			AbortWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "edge admin auth unavailable")
			return
		}

		key := extractEdgeCapacityKey(c)
		if key == "" {
			AbortWithError(c, http.StatusUnauthorized, "API_KEY_REQUIRED", "API key is required (x-api-key header or Bearer token)")
			return
		}

		apiKey, err := keys.GetByKey(c.Request.Context(), key)
		if err != nil || apiKey == nil {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
			return
		}

		user, err := users.GetByID(c.Request.Context(), apiKey.UserID)
		if err != nil || user == nil {
			AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API key owner not found")
			return
		}

		// The elevation gate: only an admin-owned, active key may mutate.
		if !user.IsAdmin() || !user.IsActive() {
			AbortWithError(c, http.StatusForbidden, "ADMIN_REQUIRED", "API key owner must be an active admin")
			return
		}

		c.Next()
	}
}
