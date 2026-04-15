package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKAdminChannelRoutes wires TokenKey New API admin endpoints without expanding upstream-shaped route tables inline.
func registerTKAdminChannelRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if admin == nil || h == nil || h.Admin == nil || h.Admin.TKChannel == nil {
		return
	}
	admin.GET("/channel-types", h.Admin.TKChannel.ListChannelTypes)
	admin.GET("/channel-type-models", h.Admin.TKChannel.ListChannelTypeModels)
	admin.POST("/channel-types/fetch-upstream-models", h.Admin.TKChannel.FetchUpstreamModels)
}

// registerTKAdminChannelNestedRoutes registers TokenKey-only routes on /admin/channels (must stay before /:id).
func registerTKAdminChannelNestedRoutes(channels *gin.RouterGroup, h *handler.Handlers) {
	if channels == nil || h == nil || h.Admin == nil || h.Admin.TKChannel == nil {
		return
	}
	channels.POST("/aggregated-group-models", h.Admin.TKChannel.AggregatedGroupModels)
}
