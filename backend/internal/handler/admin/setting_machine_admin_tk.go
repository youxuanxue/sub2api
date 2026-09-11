package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) ListMachineAdminKeys(c *gin.Context) {
	keys, err := h.settingService.ListMachineAdminKeys(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"keys": keys, "permissions": service.MachineAdminPermissions()})
}

func (h *SettingHandler) CreateMachineAdminKey(c *gin.Context) {
	var req struct {
		Name     string   `json:"name" binding:"required"`
		Scopes   []string `json:"scopes" binding:"required"`
		TTLHours int      `json:"ttl_hours" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Name, scopes and ttl_hours are required")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator session required")
		return
	}
	key, token, err := h.settingService.CreateMachineAdminKey(c.Request.Context(), subject.UserID, req.Name, req.Scopes, req.TTLHours)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"target_machine_key_id": key.ID})
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"credential": key, "key": token})
}

func (h *SettingHandler) RevokeMachineAdminKey(c *gin.Context) {
	if err := h.settingService.RevokeMachineAdminKey(c.Request.Context(), c.Param("id")); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	middleware.SetAuditExtra(c, map[string]any{"target_machine_key_id": c.Param("id")})
	response.Success(c, gin.H{"message": "Machine credential revoked"})
}
