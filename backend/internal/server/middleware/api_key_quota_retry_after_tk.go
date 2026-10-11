package middleware

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// apiKeyQuotaRetryAfterSeconds is the advisory backoff advertised on the
// api-key quota 429. A key's own ceiling only lifts on an operator quota raise
// or a billing-cycle reset, so the delay is a client-pacing hint rather than a
// prediction: long enough to collapse a hot retry loop, short enough that a
// legitimate client notices a raised quota quickly.
const apiKeyQuotaRetryAfterSeconds = 60

// setAPIKeyQuotaRetryAfter advertises the backoff on the quota 429. Follows the
// header shape already used by rejectInvalidAuthAbuse (ingress_reject.go) and
// abortPanelRateLimited (panel_rate_limit.go): integer delay-seconds, set
// before the body is written. Existing headers are left alone so a more
// specific upstream value (set by an earlier limiter on the same request) wins.
func setAPIKeyQuotaRetryAfter(c *gin.Context) {
	if c == nil {
		return
	}
	if c.Writer != nil && c.Writer.Header().Get("Retry-After") != "" {
		return
	}
	c.Header("Retry-After", strconv.Itoa(apiKeyQuotaRetryAfterSeconds))
}
