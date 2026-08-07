package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSnapshotGatewayTransferLatencyMs_SumsAuthAndRoutingOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, 12)
	SetOpsLatencyMs(c, OpsRoutingLatencyMsKey, 34)

	got := SnapshotGatewayTransferLatencyMs(c)
	require.NotNil(t, got)
	require.Equal(t, 46, *got)
}

func TestSnapshotGatewayTransferLatencyMs_IgnoresResponseTail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, 12)
	SetOpsLatencyMs(c, OpsRoutingLatencyMsKey, 34)
	SetOpsLatencyMs(c, OpsResponseLatencyMsKey, 90000)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, 100)

	got := SnapshotGatewayTransferLatencyMs(c)
	require.NotNil(t, got)
	require.Equal(t, 46, *got)
}

func TestSnapshotGatewayTransferLatencyMs_SubtractsQueueWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, 100)
	SetOpsLatencyMs(c, OpsRoutingLatencyMsKey, 2500)
	AddOpsGatewayQueueWaitMs(c, 2400)

	got := SnapshotGatewayTransferLatencyMs(c)
	require.NotNil(t, got)
	require.Equal(t, 200, *got)
}

func TestSnapshotGatewayTransferLatencyMs_SubtractsOpenAIWSQueueWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, 20)
	SetOpsLatencyMs(c, OpsRoutingLatencyMsKey, 520)
	SetOpsLatencyMs(c, OpsOpenAIWSQueueWaitMsKey, 500)

	got := SnapshotGatewayTransferLatencyMs(c)
	require.NotNil(t, got)
	require.Equal(t, 40, *got)
}

func TestRecordOpsResponseTransferLatencyMs_UsesForwardMinusUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, 9000)
	RecordOpsResponseTransferLatencyMs(c, 9050)

	responseMs, ok := contextLatencyMs(c, OpsResponseLatencyMsKey)
	require.True(t, ok)
	require.Equal(t, int64(50), responseMs)
}

func TestSnapshotGatewayTransferLatencyMs_ReturnsNilWhenUnknown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	require.Nil(t, SnapshotGatewayTransferLatencyMs(c))
	require.Nil(t, SnapshotGatewayTransferLatencyMs(nil))
}

func TestSnapshotGatewayTransferLatencyMs_ReturnsNilWhenOnlyQueueWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsAuthLatencyMsKey, 100)
	AddOpsGatewayQueueWaitMs(c, 100)

	require.Nil(t, SnapshotGatewayTransferLatencyMs(c))
}

func TestSnapshotGatewayTransferLatencyMs_ReturnsNilWhenOnlyResponseTail(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	SetOpsLatencyMs(c, OpsResponseLatencyMsKey, 50000)

	require.Nil(t, SnapshotGatewayTransferLatencyMs(c))
}
