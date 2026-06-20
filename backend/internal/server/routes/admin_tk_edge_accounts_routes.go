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

	// Thin proxy for inline edge-account WRITE ops: forwards a whitelisted op to
	// the target edge's least-privilege endpoint so an operator manages an edge's
	// accounts from the prod /accounts page without leaving it. Admin JWT inherited
	// from /admin; :edge resolves the mirror stub, :id is the edge-LOCAL account id.
	// (:edge is shared with admin-session above; the trailing /accounts/:id/<op>
	// segments are distinct, so no gin wildcard conflict.) See
	// edge_account_ops_handler_tk.go.
	if h.Admin.EdgeAccountOps != nil {
		admin.POST("/edge-accounts/:edge/accounts/:id/clear-rate-limit", h.Admin.EdgeAccountOps.ClearRateLimit)
		admin.POST("/edge-accounts/:edge/accounts/:id/reset-quota", h.Admin.EdgeAccountOps.ResetQuota)
		admin.DELETE("/edge-accounts/:edge/accounts/:id/temp-unschedulable", h.Admin.EdgeAccountOps.ClearTempUnschedulable)
		admin.POST("/edge-accounts/:edge/accounts/:id/schedulable", h.Admin.EdgeAccountOps.SetSchedulable)
		admin.GET("/edge-accounts/:edge/accounts/:id/usage", h.Admin.EdgeAccountOps.GetActiveUsage)
	}
}
