package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKUserRoutes wires every TokenKey-only authenticated endpoint
// reachable from RegisterUserRoutes' JWT-only `authenticated` group.
// One helper call from upstream-shaped user.go (CLAUDE.md §5) keeps the
// merge-conflict surface there minimal, and one helper file (this one)
// keeps every TK user-side route in a single Jobs-style canonical place
// — no scattered `*_tk_*.go` per route prefix.
//
// Endpoints (JWT-only):
//   - POST /api/v1/user/onboarding-tour-completed — US-031 P1-A.
//   - GET  /api/v1/me/pricing-catalog            — per-user "your menu"
//     view; price + capability list scoped to the API key's group, plus
//     pickers for switching between accessible groups. See
//     handler/me_pricing_catalog_handler_tk.go.
//

func registerTKUserRoutes(authenticated, user *gin.RouterGroup, h *handler.Handlers) {
	user.POST("/onboarding-tour-completed", h.User.MarkOnboardingTourSeen)
	if h.MePricingCatalog != nil {
		authenticated.GET("/me/pricing-catalog", h.MePricingCatalog.Get)
	}
	if h.APIKey != nil {
		authenticated.GET("/me/api-keys/:id/capabilities", h.APIKey.GetCapabilities)
	}
}
