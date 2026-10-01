package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	ippkg "github.com/Wei-Shaw/sub2api/internal/pkg/ip"

	"github.com/gin-gonic/gin"
)

// TokenKey: auth antifraud companions for CallModel C-end open.
//   - attachSignupClientIP: stamps security client IP into context for bonus withhold
//   - tkAuthDailyLimit: per-IP daily hard cap on register / OAuth complete

const tkAuthRegisterDailyLimit = 5

func attachSignupClientIP() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := ippkg.GetSecurityClientIP(c, false)
		if ip == "" {
			ip = c.ClientIP()
		}
		ctx := service.WithSignupClientIP(c.Request.Context(), ip)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func tkAuthDailyLimit(rateLimiter *middleware.RateLimiter, key string) gin.HandlerFunc {
	return rateLimiter.LimitWithOptions(key, tkAuthRegisterDailyLimit, 24*time.Hour, middleware.RateLimitOptions{
		FailureMode: middleware.RateLimitFailClose,
	})
}
