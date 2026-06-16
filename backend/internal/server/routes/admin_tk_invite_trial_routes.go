package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKInviteTrialRoutes registers TokenKey's Invite-to-Trial admin
// surface under the existing /admin/users group: one-step batch provisioning
// plus reusable "试用方案" preset CRUD. Kept in a *_tk_* companion so admin.go
// stays close to upstream shape (CLAUDE.md §5).
func registerTKInviteTrialRoutes(users *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.TrialProvision == nil {
		return
	}
	users.POST("/invite-trial", h.Admin.TrialProvision.InviteTrial)
	users.GET("/trial-presets", h.Admin.TrialProvision.GetTrialPresets)
	users.PUT("/trial-presets", h.Admin.TrialProvision.SetTrialPresets)
}
