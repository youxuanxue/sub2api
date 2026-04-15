package service

import (
	"testing"

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
