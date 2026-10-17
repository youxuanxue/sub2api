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
	if v1 == nil || h == nil || h.EdgeCapacity == nil || apiKeyService == nil {
		return
	}
	edge := v1.Group("/edge")
	edge.Use(middleware2.NewEdgeCapacityAuthMiddleware(apiKeyService))
	// Require the api-key owner to be an admin before exposing edge capacity or
	// Gemini Web control operations.
	if userService != nil {
		edge.Use(middleware2.NewEdgeAdminOwnerMiddleware(apiKeyService, userService))
	}
	edge.GET("/scheduling-capacity", h.EdgeCapacity.GetSchedulingCapacity)

	// Reuse the edge API-key and admin-owner gates for session control too.
	if h.GeminiWebSession != nil && h.GeminiWebSession.Enabled() && userService != nil {
		control := edge.Group("/gemini-web")
		control.GET("/accounts/:id/session", h.GeminiWebSession.Get)
		control.PUT("/accounts/:id/runtime", h.GeminiWebSession.PutRuntime)
		control.GET("/warm-accounts", h.GeminiWebSession.WarmAccounts)
		control.POST("/accounts/:id/lease", h.GeminiWebSession.Lease)
		control.DELETE("/accounts/:id/lease", h.GeminiWebSession.Lease)
	}
}
