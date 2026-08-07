package service

import (
	"github.com/gin-gonic/gin"
)

// SnapshotGatewayTransferLatencyMs derives TokenKey gateway transfer latency from
// auth, routing, and response tail timings on the gin context. It excludes upstream
// provider wait, streaming body pump time, and queue/throttle waits.
//
// Returns nil when no usable stage timing remains (excluded from dashboard average).
func SnapshotGatewayTransferLatencyMs(c *gin.Context) *int {
	if c == nil {
		return nil
	}
	authMs, hasAuth := contextLatencyMs(c, OpsAuthLatencyMsKey)
	routingMs, hasRouting := contextLatencyMs(c, OpsRoutingLatencyMsKey)
	responseMs, hasResponse := contextLatencyMs(c, OpsResponseLatencyMsKey)
	if !hasAuth && !hasRouting && !hasResponse {
		return nil
	}
	sum := authMs + routingMs + responseMs
	queueMs, _ := contextLatencyMs(c, OpsGatewayQueueWaitMsKey)
	wsQueueMs, _ := contextLatencyMs(c, OpsOpenAIWSQueueWaitMsKey)
	sum -= queueMs + wsQueueMs
	if sum <= 0 {
		return nil
	}
	v := int(sum)
	return &v
}

// RecordOpsResponseTransferLatencyMs stores gateway response tail latency as the
// post-upstream portion of forward time (format conversion, flush, billing hooks).
func RecordOpsResponseTransferLatencyMs(c *gin.Context, forwardDurationMs int64) {
	if c == nil || forwardDurationMs < 0 {
		return
	}
	responseMs := forwardDurationMs
	upstreamMs, hasUpstream := contextLatencyMs(c, OpsUpstreamLatencyMsKey)
	if hasUpstream && forwardDurationMs > upstreamMs {
		responseMs = forwardDurationMs - upstreamMs
	}
	SetOpsLatencyMs(c, OpsResponseLatencyMsKey, responseMs)
}

func contextLatencyMs(c *gin.Context, key string) (int64, bool) {
	if c == nil {
		return 0, false
	}
	v, ok := c.Get(key)
	if !ok {
		return 0, false
	}
	var ms int64
	switch t := v.(type) {
	case int:
		ms = int64(t)
	case int32:
		ms = int64(t)
	case int64:
		ms = t
	case float64:
		ms = int64(t)
	default:
		return 0, false
	}
	if ms < 0 {
		return 0, false
	}
	return ms, true
}
