package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// TkSetBridgeGinAuth stores user/group context for new-api relay bridge auth (gin context).
func TkSetBridgeGinAuth(c *gin.Context, userID int64, groupName string) {
	c.Set(service.BridgeGinAuthContextKey, service.GinBridgeAuth{UserID: userID, GroupName: groupName})
}
