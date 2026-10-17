package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterTKGeminiWebRoutes wires the internal Gemini Web worker control
// protocol. The /edge path is part of the worker protocol contract; it does
// not provision or route an edge deployment.
func RegisterTKGeminiWebRoutes(v1 *gin.RouterGroup, h *handler.Handlers, apiKeyService *service.APIKeyService, userService *service.UserService) {
	if v1 == nil || h == nil || apiKeyService == nil {
		return
	}
	control := v1.Group("/edge/gemini-web")
	control.Use(middleware2.NewGeminiWebControlAuthMiddleware(apiKeyService))
	// Require the api-key owner to be an admin before exposing worker control.
	if userService != nil {
		control.Use(middleware2.NewGeminiWebAdminOwnerMiddleware(apiKeyService, userService))
	}
	if h.GeminiWebSession != nil && h.GeminiWebSession.Enabled() && userService != nil {
		control.GET("/accounts/:id/session", h.GeminiWebSession.Get)
		control.PUT("/accounts/:id/runtime", h.GeminiWebSession.PutRuntime)
		control.GET("/warm-accounts", h.GeminiWebSession.WarmAccounts)
		control.POST("/accounts/:id/lease", h.GeminiWebSession.Lease)
		control.DELETE("/accounts/:id/lease", h.GeminiWebSession.Lease)
	}
}
