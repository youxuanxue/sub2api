package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/usagebilling"
	"github.com/gin-gonic/gin"
)

// HistoricalBilling is registered only inside the existing admin auth group.
func (h *UsageHandler) HistoricalBilling(c *gin.Context) {
	usagebilling.Serve(c, h.usageService, 0, true)
}
