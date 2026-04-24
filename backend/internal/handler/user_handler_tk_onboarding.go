package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// MarkOnboardingTourSeen handles POST /api/v1/user/onboarding-tour-completed.
// Idempotent at the repo layer (US-031 AC-007). On 5xx the frontend retries
// on next dashboard mount; `response.ErrorFrom` already logs 5xx so we do
// NOT add a second slog line here.
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
