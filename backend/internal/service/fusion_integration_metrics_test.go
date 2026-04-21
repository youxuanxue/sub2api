package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCollectFusionIntegrationMetrics(t *testing.T) {
	t.Parallel()
	m := CollectFusionIntegrationMetrics()
	require.GreaterOrEqual(t, m.BridgeDispatchTotal, int64(0))
	require.GreaterOrEqual(t, m.AffinityLookups, int64(0))
	require.GreaterOrEqual(t, m.AffinityHitRatio, 0.0)
	require.LessOrEqual(t, m.AffinityHitRatio, 1.0)
}

func TestCollectFusionIntegrationMetrics_ExposesCoreSeries(t *testing.T) {
	t.Parallel()

	firstToken := int64(180)
	ObserveFusionHTTPRequest("openai", "gpt-4o", 200, 250*time.Millisecond, &firstToken)
	RecordFusionAccountFailure("openai", 42, "5xx")
	SetFusionAccountPoolSize("openai", "active", 3)
	RecordFusionUsageBillingApplyError("apply_failed")

	m := CollectFusionIntegrationMetrics()
	require.NotEmpty(t, m.HTTPRequests)
	require.NotEmpty(t, m.HTTPRequestDurations)
	require.NotEmpty(t, m.FirstTokenDurations)
	require.NotEmpty(t, m.AccountFailures)
	require.NotEmpty(t, m.AccountPoolSizes)
	require.NotEmpty(t, m.UsageBillingApplyErrors)
}
