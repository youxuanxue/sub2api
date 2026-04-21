package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterMetricsRoute registers the Prometheus metrics endpoint (admin protected).
func RegisterMetricsRoute(r *gin.Engine, adminAuth middleware.AdminAuthMiddleware) {
	r.GET("/metrics", gin.HandlerFunc(adminAuth), handler.PrometheusMetrics)
}
