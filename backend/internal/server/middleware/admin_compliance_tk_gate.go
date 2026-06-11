package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkAdminComplianceGuardIfEnabled wraps the upstream AdminComplianceGuard
// behind the TokenKey default-off setting (see
// service.SettingKeyTkAdminComplianceGateEnabled). With the gate disabled the
// request passes straight through, keeping TokenKey's non-interactive admin
// automation (admin_api_key, forged edge JWTs, SSM ops scripts) working
// without a per-node acknowledgement step.
func TkAdminComplianceGuardIfEnabled(settingService *service.SettingService) gin.HandlerFunc {
	guard := AdminComplianceGuard(settingService)
	return func(c *gin.Context) {
		if settingService == nil || !settingService.IsTkAdminComplianceGateEnabled(c.Request.Context()) {
			c.Next()
			return
		}
		guard(c)
	}
}
