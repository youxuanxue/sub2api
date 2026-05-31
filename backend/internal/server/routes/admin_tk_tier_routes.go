package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKAccountTierRoutes registers TokenKey's per-account tier-apply route.
// Kept in a *_tk_* companion so admin.go stays close to upstream shape.
//
// Only a POST is registered (no GET /tiers): the l1..l5 tier set is stable and
// lives in a frontend constant, which also avoids a gin static-vs-:id route
// conflict under the /accounts group.
func registerTKAccountTierRoutes(accounts *gin.RouterGroup, h *handler.Handlers) {
	accounts.POST("/:id/apply-tier", h.Admin.Account.ApplyTier)
}

// registerTKTierTemplateRoutes registers the anthropic-oauth stability tier
// reference-table CRUD (the "tier 模板" management surface, mirroring the TLS
// fingerprint profile routes). tiers are a projection of the git baseline; UI
// edits are emergency/local and re-asserted by the ops/anthropic pipeline.
func registerTKTierTemplateRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.Tier == nil {
		return
	}
	tiers := admin.Group("/tiers")
	tiers.GET("", h.Admin.Tier.List)
	tiers.GET("/:id", h.Admin.Tier.GetByID)
	tiers.POST("", h.Admin.Tier.Create)
	tiers.PUT("/:id", h.Admin.Tier.Update)
	tiers.DELETE("/:id", h.Admin.Tier.Delete)
}
