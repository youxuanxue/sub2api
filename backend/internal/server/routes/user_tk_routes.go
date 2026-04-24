package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKUserRoutes wires TokenKey-only authenticated user endpoints under
// /api/v1/user/*. Kept in a companion file (per CLAUDE.md §5) so
// RegisterUserRoutes in routes/user.go only carries a single helper call,
// not multi-line route bodies — minimizes upstream merge friction whenever
// Wei-Shaw/sub2api evolves the user route table.
//
// Currently registers:
//   - POST /user/onboarding-tour-completed — persist server-side "已看过"
//     so the onboarding tour does not auto-launch again across devices /
//     after a localStorage clear (US-031, docs/approved/user-cold-start.md §5 P1-A).
//     Idempotent at the repo layer (UPDATE … WHERE seen_at IS NULL).
func registerTKUserRoutes(user *gin.RouterGroup, h *handler.Handlers) {
	if user == nil || h == nil || h.User == nil {
		return
	}
	user.POST("/onboarding-tour-completed", h.User.MarkOnboardingTourSeen)
}
