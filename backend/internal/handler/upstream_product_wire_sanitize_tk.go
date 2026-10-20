package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkSanitizeUpstreamProductWireMessage is the handler-layer choke for public
// error copy that would expose upstream plan/product branding (Agent Plan,
// Token Plan, vendor docs URLs). Ops keeps the original detail via first-wins
// MarkOpsStreamError when a rewrite happens.
func tkSanitizeUpstreamProductWireMessage(c *gin.Context, status int, errType, message string) string {
	public, sanitized := service.TkSanitizeUpstreamProductWireMessage(message)
	if !sanitized {
		return public
	}
	if errType == "" {
		errType = "invalid_request_error"
	}
	service.TkAnnotateUpstreamProductOps(c, errType, status, message)
	return public
}
