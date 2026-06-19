package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKAccountUsageBatchRoutes registers TokenKey's batch passive-usage
// endpoint for the admin accounts list. Kept in a *_tk_* companion so admin.go
// stays close to upstream shape.
//
// POST /admin/accounts/usage/batch collapses the per-row GET /:id/usage fan-out
// (one XHR per OAuth/SetupToken/Gemini row) into a single request whose result
// the frontend injects via AccountUsageCell's usageOverride prop. Mirrors the
// existing POST /accounts/today-stats/batch shape (static segment under
// /accounts, no /:id conflict).
func registerTKAccountUsageBatchRoutes(accounts *gin.RouterGroup, h *handler.Handlers) {
	if accounts == nil || h == nil || h.Admin == nil || h.Admin.Account == nil {
		return
	}
	accounts.POST("/usage/batch", h.Admin.Account.GetBatchPassiveUsage)
}
