package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine) {
	// /health: 就绪探针（readiness）。
	// drain 模式下返回 503 + 当前 in-flight，Caddy 的 health_passive 据此立刻摘除 upstream，
	// 同时让 deploy 脚本可以通过它确认 SIGUSR1 已生效。
	r.GET("/health", func(c *gin.Context) {
		if middleware.IsDraining() {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":    "draining",
				"in_flight": middleware.InFlightCount(),
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// /health/live: 存活探针（liveness）。
	// 进程在跑就 200，不受 drain 影响——Caddy 的 health_uri 指向这个端点，
	// 避免主动健康检查误判「drain=down」从而过早把容器移出。
	r.GET("/health/live", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "alive"})
	})

	// /health/inflight: 排空遥测。
	// 发版脚本 docker exec 进容器轮询此值，等到 in_flight=0 再真正停容器。
	// 不暴露任何业务信息，纯计数。
	r.GET("/health/inflight", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"in_flight": middleware.InFlightCount(),
			"draining":  middleware.IsDraining(),
		})
	})

	// Claude Code 遥测日志（忽略，直接返回200）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
