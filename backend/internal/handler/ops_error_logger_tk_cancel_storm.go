package handler

// TK: thin chokepoint shim for the per-API-key cancel-storm detector.
//
// OpsErrorLoggerMiddleware runs once per inbound /v1 request after c.Next(), on
// both the success and >=400 branches, so it is the single terminal-outcome
// observation point. Internal retries/failover resolve to one terminal outcome
// here, so each client request is counted exactly once. The heavy lifting (window
// counting, threshold, Feishu alert) lives in service.OpsService.ObserveCancelStorm
// and is a no-op unless cancel_storm_config mode is "detect_only".

import (
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func tkObserveCancelStorm(c *gin.Context, ops *service.OpsService) {
	if c == nil || ops == nil {
		return
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.ID == 0 {
		return
	}
	model := ""
	if v, exists := c.Get(opsModelKey); exists {
		if s, ok := v.(string); ok {
			model = s
		}
	}
	ops.ObserveCancelStorm(apiKey.ID, apiKey.Name, model, tkUpstreamClientCanceled(c))
}
