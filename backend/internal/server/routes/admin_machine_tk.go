package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func registerMachineAdminKeyRoutes(admin *gin.RouterGroup, h *handler.Handlers, stepUp middleware.StepUpAuthMiddleware) {
	keys := admin.Group("/settings/machine-admin-keys", middleware.RequireHumanAdmin())
	keys.GET("", h.Admin.Setting.ListMachineAdminKeys)
	keys.POST("", gin.HandlerFunc(stepUp), h.Admin.Setting.CreateMachineAdminKey)
	keys.DELETE("/:id", gin.HandlerFunc(stepUp), h.Admin.Setting.RevokeMachineAdminKey)
}
