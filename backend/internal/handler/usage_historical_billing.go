package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/usagebilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// HistoricalBilling always derives the tenant from the authenticated subject.
func (h *UsageHandler) HistoricalBilling(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	usagebilling.Serve(c, h.usageService, subject.UserID, false)
}
