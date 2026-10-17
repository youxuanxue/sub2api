package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkUpstreamDownstreamCapacity classifies a configured upstream gateway
// capacity envelope as routing failure. Provider rate limits and infrastructure
// errors without a gateway capacity envelope remain provider-owned.
func tkUpstreamDownstreamCapacity(c *gin.Context) bool {
	if c == nil {
		return false
	}
	// Only a relayed verdict carries a definitive upstream status. 429 is the
	// pool-empty fast-fail (PR #575); 5xx covers the legacy 503 and
	// failover-exhausted envelopes. A status of 0 (pure transport error) is owned by
	// tkUpstreamClientCanceled, not here.
	status := tkOpsUpstreamStatusCode(c)
	if status != 429 && status < 500 {
		return false
	}
	body, msg := tkOpsUpstreamErrorText(c)
	combined := strings.ToLower(strings.TrimSpace(msg + "\n" + body))
	if combined == "" {
		return false
	}
	// isOpsNoAvailableAccountMessage already lowercases; the second check uses the
	// service-owned exact matcher for current and rolling-version failover envelopes.
	return isOpsNoAvailableAccountMessage(combined) ||
		service.IsGatewayFailoverMessage(msg, []byte(body))
}
