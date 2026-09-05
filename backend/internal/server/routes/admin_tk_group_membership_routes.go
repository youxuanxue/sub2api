package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKGroupMembershipRoutes registers the group membership panel
// (account_groups SSOT view/add/remove) under /admin/groups/:id/accounts.
func registerTKGroupMembershipRoutes(groups *gin.RouterGroup, h *handler.Handlers) {
	groups.GET("/:id/accounts", h.Admin.Group.ListAccounts)
	groups.POST("/:id/accounts", h.Admin.Group.BindAccounts)
	groups.DELETE("/:id/accounts", h.Admin.Group.UnbindAccounts)
}
