package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKEdgeAccountsRoutes registers TokenKey's prod-side cross-edge
// read-only account overview:
//
//	GET /api/v1/admin/edge-accounts?platform=anthropic
//
// Kept in a *_tk_* companion so admin.go takes a single call and stays close to
// upstream shape. Auth is the admin JWT (inherited from the /admin group) — the
// aggregation across the fleet is admin-only. See edge_accounts_handler_tk.go.
func registerTKEdgeAccountsRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.EdgeAccounts == nil {
		return
	}
	admin.GET("/edge-accounts", h.Admin.EdgeAccounts.List)
	// Mint a handoff URL to manage accounts on a specific edge (jump + auto-login
	// into that edge's own /admin/accounts). Admin JWT inherited from /admin.
	admin.POST("/edge-accounts/:edge/admin-session", h.Admin.EdgeAccounts.MintAdminSession)
}
