package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKUserRoutes wires every TokenKey-only authenticated endpoint
// reachable from RegisterUserRoutes. One helper call from upstream-shaped
// user.go (CLAUDE.md §5) keeps the merge-conflict surface there minimal,
// and one helper file (this one) keeps every TK user-side route in a
// single Jobs-style canonical place — no scattered `*_tk_*.go` per
// route prefix.
//
// Endpoints:
//   - POST /api/v1/user/onboarding-tour-completed — US-031 P1-A.
//   - POST /api/v1/users/me/qa/export — issue #59 Gap 1, REST users/me
//     style to match the M0 dual-CC client contract verbatim. Auth is
//     user-scope JWT; service layer enforces `WHERE user_id = subject.UserID`.
func registerTKUserRoutes(authenticated, user *gin.RouterGroup, h *handler.Handlers) {
	user.POST("/onboarding-tour-completed", h.User.MarkOnboardingTourSeen)

	if h != nil && h.QA != nil {
		authenticated.POST("/users/me/qa/export", h.QA.ExportSelf)
	}
}
