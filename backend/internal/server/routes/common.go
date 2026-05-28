package routes

import (
	"net"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine) {
	// /health: 就绪探针（readiness）。
	// drain 模式下 503，Caddy 的 passive 检查据此立刻摘除 upstream。
	// 注意：body 不带 in_flight 数；那是内部状态，由 /health/inflight 提供，
	// /health 是公开端点，只反映 ready/draining 二态。
	r.GET("/health", func(c *gin.Context) {
		if middleware.IsDraining() {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "draining"})
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

	// /health/inflight: 排空遥测，**仅限 loopback**。
	// 发版脚本走 `docker exec tokenkey wget http://localhost:8080/...` 命中容器
	// 自己的 loopback，RemoteAddr=127.0.0.1；Caddy → container 走 docker bridge
	// IP（172.x），命中下面的 404 分支。这样 in_flight 计数不会经公网暴露。
	r.GET("/health/inflight", func(c *gin.Context) {
		if !isLoopbackRemote(c.Request.RemoteAddr) {
			c.Status(http.StatusNotFound)
			return
		}
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

// isLoopbackRemote 判断 http.Request.RemoteAddr 是否来自本地回环。
// 使用 RemoteAddr 而不是 c.ClientIP() 是故意的：ClientIP 会走 trusted_proxies
// 链跳过 X-Forwarded-For，本地探针不需要那一层；RemoteAddr 是 raw TCP 源，
// docker exec 进 container 走 loopback 直接命中。
func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		// 没带 port 直接尝试整串
		host = remoteAddr
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
