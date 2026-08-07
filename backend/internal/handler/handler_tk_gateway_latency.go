package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func tkRecordAuthLatency(c *gin.Context, requestStart time.Time) {
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
}

func tkRecordRoutingLatency(c *gin.Context, routingStart time.Time) {
	service.SetOpsLatencyMs(c, service.OpsRoutingLatencyMsKey, time.Since(routingStart).Milliseconds())
}

func tkRecordForwardResponseTail(c *gin.Context, forwardStart time.Time) {
	service.RecordOpsResponseTransferLatencyMs(c, time.Since(forwardStart).Milliseconds())
}

func tkSnapshotGatewayTransferLatencyMs(c *gin.Context) *int {
	return service.SnapshotGatewayTransferLatencyMs(c)
}

func tkRecordGatewayQueueWait(c *gin.Context, waitStart time.Time) {
	service.AddOpsGatewayQueueWaitMs(c, time.Since(waitStart).Milliseconds())
}
