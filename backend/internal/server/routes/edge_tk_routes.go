package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterTKEdgeRoutes wires TokenKey's internal edge capacity endpoint:
//
//	GET /api/v1/edge/scheduling-capacity?platform=anthropic
//
// It is the read side of surface C (per docs/CLAUDE.md fifth-platform notes /
// the anthropic-config plan): prod's reconciler calls it over HTTP to mirror a
// live edge's Σ schedulable concurrency onto the prod stub account, using the
// stub's already-held relay api-key (zero new secret).
//
// Mounted behind a dedicated lightweight api-key middleware — NOT the gateway
// billing/concurrency chain — so the cross-deployment read carries no scheduling
// side effects. Kept in a *_tk_* companion so router.go takes a single call.
func RegisterTKEdgeRoutes(v1 *gin.RouterGroup, h *handler.Handlers, apiKeyService *service.APIKeyService, userService *service.UserService) {
	if v1 == nil || h == nil || h.EdgeCapacity == nil || apiKeyService == nil {
		return
	}
	edge := v1.Group("/edge")
	edge.Use(middleware2.NewEdgeCapacityAuthMiddleware(apiKeyService))
	// TK security hardening (REVERTIBLE — see below): require the api-key OWNER to be
	// an admin for ALL edge endpoints, not just the writes. Closes the pre-existing
	// gap where any active (even non-admin) api-key could enumerate the cross-edge
	// fleet inventory / capacity. Safe because prod's reconciler + aggregator reach
	// every edge with the mirror-stub relay key, which is admin-owned BY CONSTRUCTION
	// — the /admin-session handoff already requires user.IsAdmin() on that exact key
	// and works in prod, so a non-admin stub key would already have a broken handoff.
	// To revert (if some edge's stub key is unexpectedly non-admin and its capacity
	// mirror starts 403ing): delete this single block — the write-ops subgroup below
	// keeps its OWN admin-owner gate, so reverting never weakens write protection.
	if userService != nil {
		edge.Use(middleware2.NewEdgeAdminOwnerMiddleware(apiKeyService, userService))
	}
	edge.GET("/scheduling-capacity", h.EdgeCapacity.GetSchedulingCapacity)

	// Read-only account inventory for prod's cross-edge admin overview. Same
	// lightweight api-key auth, same side-effect-free posture as capacity. The
	// handler returns a credential-free DTO; see edge_tk_accounts_handler.go.
	if h.EdgeAccounts != nil {
		edge.GET("/accounts", h.EdgeAccounts.ListAccounts)
	}

	// Mint a short-lived admin JWT for prod's "manage accounts" handoff. Same
	// lightweight api-key auth, but the handler additionally requires the key's
	// owner to be an admin before minting — see edge_tk_admin_session_handler.go.
	if h.EdgeAdminSession != nil {
		edge.POST("/admin-session", h.EdgeAdminSession.Mint)
	}

	// Least-privilege account WRITE ops the prod /accounts page proxies to for
	// inline edge-account management (clear-rate-limit / reset-quota /
	// temp-unschedulable / schedulable / active usage query). A WHITELIST that
	// never touches credentials — credential-class ops stay behind the
	// admin-session handoff above. Layered on the active-key check with an extra
	// admin-owner gate (NewEdgeAdminOwnerMiddleware): a plain relay key can read
	// the inventory but only an admin-owned key may mutate. :id is the edge-LOCAL
	// account id. See edge_tk_account_ops_handler.go.
	//
	// Path note: GET /edge/accounts (the inventory leaf above) and the
	// /edge/accounts/:id/<op> children coexist in gin's tree — distinct depths,
	// :id used consistently, no static sibling at the :id position, so no
	// wildcard conflict.
	if h.EdgeAccountOps != nil && userService != nil {
		ops := edge.Group("/accounts")
		ops.Use(middleware2.NewEdgeAdminOwnerMiddleware(apiKeyService, userService))
		ops.POST("/:id/clear-rate-limit", h.EdgeAccountOps.ClearRateLimit)
		ops.POST("/:id/reset-quota", h.EdgeAccountOps.ResetQuota)
		ops.DELETE("/:id/temp-unschedulable", h.EdgeAccountOps.ClearTempUnschedulable)
		ops.POST("/:id/schedulable", h.EdgeAccountOps.SetSchedulable)
		ops.GET("/:id/usage", h.EdgeAccountOps.GetActiveUsage)
	}
}
