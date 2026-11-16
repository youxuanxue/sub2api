package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type stubGatewayInFlightGauge struct {
	count int64
	max   int
}

func (s stubGatewayInFlightGauge) Count() int64 { return s.count }
func (s stubGatewayInFlightGauge) Max() int     { return s.max }

func TestGatewayInFlightUsagePercentMath(t *testing.T) {
	t.Parallel()

	pct, ok := gatewayInFlightUsagePercent(410, 512)
	require.True(t, ok)
	require.InDelta(t, 80.078, pct, 0.01)

	_, ok = gatewayInFlightUsagePercent(0, 0)
	require.False(t, ok, "disabled gate must not report a usage percent")

	pct, ok = gatewayInFlightUsagePercent(0, 512)
	require.True(t, ok)
	require.Equal(t, 0.0, pct)
}

func TestComputeGatewayInFlightUsagePercent_TakesHottestNode(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := &OpsAlertEvaluatorService{
		redisClient:          rdb,
		instanceID:           "leader-node",
		gatewayInFlightGauge: stubGatewayInFlightGauge{count: 100, max: 512}, // ~19.5%
	}

	quiet, err := json.Marshal(gatewayInFlightSample{Count: 50, Max: 512, TS: time.Now().Unix()})
	require.NoError(t, err)
	hot, err := json.Marshal(gatewayInFlightSample{Count: 480, Max: 512, TS: time.Now().Unix()})
	require.NoError(t, err)
	require.NoError(t, rdb.Set(context.Background(), gatewayInFlightSampleKey("app-a"), quiet, time.Minute).Err())
	require.NoError(t, rdb.Set(context.Background(), gatewayInFlightSampleKey("app-b"), hot, time.Minute).Err())

	pct, ok := svc.computeGatewayInFlightUsagePercent(context.Background())
	require.True(t, ok)
	require.InDelta(t, 93.75, pct, 0.01)
}

func TestPublishGatewayInFlight_SkipsWhenGateDisabled(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := &OpsAlertEvaluatorService{
		redisClient:          rdb,
		instanceID:           "node-1",
		gatewayInFlightGauge: stubGatewayInFlightGauge{count: 0, max: 0},
	}
	svc.publishGatewayInFlight(context.Background())
	require.Equal(t, 0, len(mr.Keys()))
}

func TestPublishAndComputeLocalSample(t *testing.T) {
	t.Parallel()

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	svc := &OpsAlertEvaluatorService{
		redisClient:          rdb,
		instanceID:           "node-1",
		gatewayInFlightGauge: stubGatewayInFlightGauge{count: 410, max: 512},
	}
	svc.publishGatewayInFlight(context.Background())

	pct, ok := svc.computeGatewayInFlightUsagePercent(context.Background())
	require.True(t, ok)
	require.InDelta(t, 80.078, pct, 0.01)
}

func TestComputeRuleMetric_GatewayInFlight(t *testing.T) {
	t.Parallel()

	svc := &OpsAlertEvaluatorService{
		gatewayInFlightGauge: stubGatewayInFlightGauge{count: 486, max: 512},
	}
	value, ok := svc.computeRuleMetric(
		context.Background(),
		&OpsAlertRule{MetricType: OpsAlertMetricGatewayInFlightUsagePercent},
		nil,
		time.Time{},
		time.Time{},
		"",
		nil,
		1,
	)
	require.True(t, ok)
	require.InDelta(t, 94.92, value, 0.01)

	disabled := &OpsAlertEvaluatorService{
		gatewayInFlightGauge: stubGatewayInFlightGauge{count: 0, max: 0},
	}
	_, ok = disabled.computeRuleMetric(
		context.Background(),
		&OpsAlertRule{MetricType: OpsAlertMetricGatewayInFlightUsagePercent},
		nil,
		time.Time{},
		time.Time{},
		"",
		nil,
		1,
	)
	require.False(t, ok)
}
