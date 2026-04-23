package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/middleware"

	"github.com/gin-gonic/gin"
)

// registerTKPublicRoutes wires TokenKey-only public (unauthenticated) endpoints.
// Kept in a companion file (per CLAUDE.md §5) so RegisterAuthRoutes in routes/auth.go
// only carries a single helper call, not multi-line route bodies — minimizes
// upstream merge friction whenever Wei-Shaw/sub2api evolves the auth route table.
//
// Currently registers:
//   - GET /api/v1/public/pricing — public pricing catalog (US-028 / docs/approved/user-cold-start.md §2 v1).
//     60 req/min/IP, FailOpen — read-only metadata, must not block on Redis outage.
func registerTKPublicRoutes(v1 *gin.RouterGroup, h *handler.Handlers, rateLimiter *middleware.RateLimiter) {
	if v1 == nil || h == nil || h.PricingCatalog == nil || rateLimiter == nil {
		return
	}
	public := v1.Group("/public")
	public.GET("/pricing",
		rateLimiter.LimitWithOptions("public-pricing", 60, time.Minute, middleware.RateLimitOptions{
			FailureMode: middleware.RateLimitFailOpen,
		}),
		h.PricingCatalog.GetPublicCatalog,
	)
}
