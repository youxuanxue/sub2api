package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKUserRoutes wires TokenKey-only authenticated user endpoints
// under /api/v1/user/*. Companion file per CLAUDE.md §5 so the upstream
// RegisterUserRoutes only carries one helper call.
//
//   - POST /user/onboarding-tour-completed — US-031 P1-A.
func registerTKUserRoutes(user *gin.RouterGroup, h *handler.Handlers) {
	user.POST("/onboarding-tour-completed", h.User.MarkOnboardingTourSeen)
}
