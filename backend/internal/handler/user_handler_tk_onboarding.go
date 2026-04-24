package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// MarkOnboardingTourSeen handles POST /api/v1/user/onboarding-tour-completed.
//
// Records server-side that the authenticated user has completed the onboarding
// tour so the dashboard does not auto-launch it again on this or any other
// device. Idempotent at the repo layer (US-031 AC-007).
//
// On 5xx the frontend (`useOnboardingTour.markAsSeen`) treats the failure as
// "not yet seen" and retries on the next dashboard mount, so a transient DB
// error never silently strands the user. `response.ErrorFrom` already logs
// 5xx with method+path+redacted error — we deliberately do NOT add a second
// slog line here to avoid duplicate log output for the same event.
func (h *UserHandler) MarkOnboardingTourSeen(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if err := h.userService.MarkOnboardingTourSeen(c.Request.Context(), subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"success": true})
}
