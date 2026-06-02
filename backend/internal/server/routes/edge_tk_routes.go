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
func RegisterTKEdgeRoutes(v1 *gin.RouterGroup, h *handler.Handlers, apiKeyService *service.APIKeyService) {
	if v1 == nil || h == nil || h.EdgeCapacity == nil || apiKeyService == nil {
		return
	}
	edge := v1.Group("/edge")
	edge.Use(middleware2.NewEdgeCapacityAuthMiddleware(apiKeyService))
	edge.GET("/scheduling-capacity", h.EdgeCapacity.GetSchedulingCapacity)

	// Read-only account inventory for prod's cross-edge admin overview. Same
	// lightweight api-key auth, same side-effect-free posture as capacity. The
	// handler returns a credential-free DTO; see edge_tk_accounts_handler.go.
	if h.EdgeAccounts != nil {
		edge.GET("/accounts", h.EdgeAccounts.ListAccounts)
	}
}
