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
// It is the read side of the Anthropic config reconciliation surface C:
// prod's reconciler calls it over HTTP to mirror a
// live edge's Σ schedulable concurrency onto the prod stub account, using the
// stub's already-held relay api-key (zero new secret).
//
// Mounted behind a dedicated lightweight api-key middleware — NOT the gateway
// billing/concurrency chain — so the cross-deployment read carries no scheduling
// side effects. Kept in a *_tk_* companion so router.go takes a single call.
func RegisterTKEdgeRoutes(v1 *gin.RouterGroup, h *handler.Handlers, apiKeyService *service.APIKeyService, userService *service.UserService) {
	// Signed handoff is independent of mirror-key middleware and gateway billing.
	if v1 != nil && h != nil && h.EdgeAdminSession != nil {
		v1.GET("/edge/admin-handoff/configuration", h.EdgeAdminSession.Configuration)
		v1.POST("/edge/admin-handoff/mint", h.EdgeAdminSession.MintCode)
		v1.POST("/edge/admin-handoff/exchange", h.EdgeAdminSession.Exchange)
		v1.POST("/edge/admin-session", h.EdgeAdminSession.Mint)
	}
	if v1 == nil || h == nil || h.EdgeCapacity == nil || apiKeyService == nil {
		return
	}
	edge := v1.Group("/edge")
	edge.Use(middleware2.NewEdgeCapacityAuthMiddleware(apiKeyService))
	// Existing mirror inventory and operational writes require an active admin
	// owner. These relay keys never authorize the independent session handoff.
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

	// Least-privilege account WRITE ops the prod /accounts page proxies to for
	// inline edge-account management (clear-rate-limit / reset-quota /
	// temp-unschedulable / schedulable / active usage query). A WHITELIST that
	// never touches credentials — credential-class ops stay behind the
	// signed handoff above. Layered on the active-key check with an extra
	// admin-owner gate (NewEdgeAdminOwnerMiddleware): only an admin-owned relay key may read
	// the inventory or mutate. :id is the edge-LOCAL
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
