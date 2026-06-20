//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func opsAlertRawMsg(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

// TestValidateOpsAlertRulePayloadPoolLoadRateAccepted pins R-003: pool_load_rate
// is evaluator-supported (first switch) and offered by the admin UI metric
// registry, so the API must accept it. Before the fix it was missing from
// validOpsAlertMetricTypes and a create/edit was rejected with 400.
func TestValidateOpsAlertRulePayloadPoolLoadRateAccepted(t *testing.T) {
	raw := map[string]json.RawMessage{
		"name":        opsAlertRawMsg(t, "账号池容量触顶"),
		"metric_type": opsAlertRawMsg(t, "pool_load_rate"),
		"operator":    opsAlertRawMsg(t, ">="),
		"threshold":   opsAlertRawMsg(t, 90.0),
	}
	got, err := validateOpsAlertRulePayload(raw)
	require.NoError(t, err)
	require.Equal(t, "pool_load_rate", got.MetricType)
}

// TestIsPercentOrRateMetricClassifications pins the threshold-bounding contract:
// pool_load_rate is a % gauge (bounded 0-100), while
// routing_capacity_rejection_count is a COUNT and must NOT be clamped to 0-100
// (its storm threshold is >=50 and counts can far exceed 100).
func TestIsPercentOrRateMetricClassifications(t *testing.T) {
	require.True(t, isPercentOrRateMetric("pool_load_rate"))
	require.False(t, isPercentOrRateMetric("routing_capacity_rejection_count"))
}

// TestValidateOpsAlertRulePayloadCountThresholdNotClampedTo100 guards that the
// new count metric accepts a threshold above 100 (a percent/rate metric would be
// rejected). This is why routing_capacity_rejection_count is deliberately NOT in
// isPercentOrRateMetric.
func TestValidateOpsAlertRulePayloadCountThresholdNotClampedTo100(t *testing.T) {
	raw := map[string]json.RawMessage{
		"name":        opsAlertRawMsg(t, "无可用账号拒绝激增"),
		"metric_type": opsAlertRawMsg(t, "routing_capacity_rejection_count"),
		"operator":    opsAlertRawMsg(t, ">="),
		"threshold":   opsAlertRawMsg(t, 250.0),
	}
	got, err := validateOpsAlertRulePayload(raw)
	require.NoError(t, err)
	require.EqualValues(t, 250, got.Threshold)
}
