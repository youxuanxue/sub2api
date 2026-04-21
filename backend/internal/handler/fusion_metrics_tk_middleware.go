package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// FusionMetricsMiddleware records low-overhead Prometheus-friendly gateway metrics.
func FusionMetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()

		platform := getFusionMetricsPlatform(c)
		model := getFusionMetricsModel(c)
		status := c.Writer.Status()

		var firstTokenMs *int64
		if value, ok := c.Get(service.OpsTimeToFirstTokenMsKey); ok {
			if parsed, ok := value.(int64); ok && parsed > 0 {
				firstTokenMs = &parsed
			}
		}

		service.ObserveFusionHTTPRequest(platform, model, status, time.Since(startedAt), firstTokenMs)

		if status >= 400 {
			if accountID, ok := getFusionMetricsAccountID(c); ok && accountID > 0 {
				service.RecordFusionAccountFailure(platform, accountID, fusionFailureReason(status))
			}
		}
	}
}

func getFusionMetricsPlatform(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if forcePlatform, ok := middleware.GetForcePlatformFromContext(c); ok && forcePlatform != "" {
		return forcePlatform
	}
	if c.Request != nil {
		if platform, ok := c.Request.Context().Value(ctxkey.Platform).(string); ok && platform != "" {
			return platform
		}
	}
	if apiKey, ok := middleware.GetAPIKeyFromContext(c); ok && apiKey != nil && apiKey.Group != nil {
		return apiKey.Group.Platform
	}
	return ""
}

func getFusionMetricsModel(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(opsModelKey)
	if !ok {
		return ""
	}
	model, _ := value.(string)
	return model
}

func getFusionMetricsAccountID(c *gin.Context) (int64, bool) {
	if c == nil {
		return 0, false
	}
	value, ok := c.Get(opsAccountIDKey)
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

func fusionFailureReason(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status == 429:
		return "rate_limit"
	case status >= 400:
		return "4xx"
	default:
		return "unknown"
	}
}
