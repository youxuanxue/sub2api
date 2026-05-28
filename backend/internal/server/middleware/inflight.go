package middleware

import (
	"strings"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// 进程级别的发版排空状态：
//   - drainFlag=true 时，/health 返回 503，外层 Caddy 立刻把 upstream 摘除，
//     新连接进入 lb_try_duration 队列；正在跑的请求继续完成。
//   - inflight 仅统计业务请求（跳过 /health* 自探测路径），用于 deploy 脚本
//     轮询「现在还有几个长流没结束」决定何时真正停容器。
//
// 这里不引入 context、不持锁、不分配，确保 gateway 高 QPS 下零额外开销。
var (
	drainFlag atomic.Bool
	inflight  atomic.Int64
)

// SetDrain 把 drainFlag 切到指定状态。
// 发版流程通过 SIGUSR1 → SetDrain(true)，让 /health 立刻翻 503。
// SIGTERM 收到时也会兜底再调用一次，保证「优雅停机」与「pre-drain」共用同一开关。
func SetDrain(v bool) {
	drainFlag.Store(v)
}

// IsDraining 当前是否处于排空状态。
func IsDraining() bool {
	return drainFlag.Load()
}

// InFlightCount 当前业务请求的并发数（不含 /health* 自探测）。
func InFlightCount() int64 {
	return inflight.Load()
}

// InFlightTracker 统计非自探测请求的并发数，并在请求结束时减一。
// gin 的 c.Next() 对 SSE / streaming 请求会阻塞到响应体写完才返回，所以 defer
// 也会在长流结束（或客户端断开）时执行；这正是 deploy 脚本需要等到 0 的口径。
func InFlightTracker() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || isHealthProbePath(c.Request.URL.Path) {
			c.Next()
			return
		}
		inflight.Add(1)
		defer inflight.Add(-1)
		c.Next()
	}
}

// isHealthProbePath 判断是否为自探测路径，需要从 inflight 统计里排除。
// 与 routes/common.go 里实际注册的 path 保持一致。
func isHealthProbePath(p string) bool {
	if p == "" {
		return false
	}
	// 精确匹配三个 health endpoint；用 HasPrefix 防御后续可能新增的子路径。
	return p == "/health" || strings.HasPrefix(p, "/health/")
}
