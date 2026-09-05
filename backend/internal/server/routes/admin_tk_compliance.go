package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// applyTKAdminComplianceMiddleware installs the setting-gated TokenKey compliance
// gate (default off). Upstream AdminComplianceGuard still runs after this.
func applyTKAdminComplianceMiddleware(admin *gin.RouterGroup, settingService *service.SettingService) {
	admin.Use(middleware.TkAdminComplianceGuardIfEnabled(settingService))
}
