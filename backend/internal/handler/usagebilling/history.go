// Package usagebilling owns the shared historical-billing HTTP query contract.
package usagebilling

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func Serve(c *gin.Context, svc *service.UsageService, userID int64, admin bool) {
	// This is a different data grain from request logs. Reject inherited request
	// filters instead of silently presenting an unfiltered financial report.
	allowed := map[string]bool{"start_date": true, "end_date": true, "granularity": true, "platform": true, "timezone": true}
	if admin {
		allowed["user_id"] = true
	}
	for key, values := range c.Request.URL.Query() {
		if !allowed[key] || len(values) != 1 {
			response.BadRequest(c, "Unsupported or repeated billing filter: "+key)
			return
		}
	}
	if admin {
		var err error
		userID, err = strconv.ParseInt(c.Query("user_id"), 10, 64)
		if err != nil || userID <= 0 {
			response.BadRequest(c, "A positive user_id is required")
			return
		}
	}
	query := usagestats.HistoricalBillingQuery{
		StartDate: strings.TrimSpace(c.Query("start_date")), EndDate: strings.TrimSpace(c.Query("end_date")),
		Granularity: strings.TrimSpace(c.Query("granularity")),
	}
	if values, exists := c.Request.URL.Query()["platform"]; exists {
		platform := values[0]
		if len(platform) > 100 {
			response.BadRequest(c, "Invalid platform")
			return
		}
		query.Platform = &platform
	}
	report, err := svc.GetHistoricalBilling(c.Request.Context(), userID, query)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	c.Header("Cache-Control", "private, no-store")
	response.Success(c, report)
}
