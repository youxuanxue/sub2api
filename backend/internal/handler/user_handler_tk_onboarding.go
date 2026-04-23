package handler

import (
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// MarkOnboardingTourSeen handles POST /api/v1/user/onboarding-tour-completed.
// Records server-side that the authenticated user has completed the onboarding
// tour so the dashboard does not auto-launch it again on this or any other
// device. Idempotent: a second call is a no-op (US-031 AC-007).
func (h *UserHandler) MarkOnboardingTourSeen(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if err := h.userService.MarkOnboardingTourSeen(c.Request.Context(), subject.UserID); err != nil {
		// Best-effort: server log + 500 so the client can retry on the next mount.
		// The frontend treats a failure as "not yet seen" so the next dashboard
		// load will retry the POST — preventing silent permanent failure.
		slog.Error("[Onboarding] mark_seen_failed",
			slog.Int64("user_id", subject.UserID),
			slog.String("err", err.Error()),
		)
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, gin.H{"ok": true})
}
