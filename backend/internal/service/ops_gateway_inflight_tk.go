package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// OpsAlertMetricGatewayInFlightUsagePercent is the multi-app fan-in of each
// process's relay in-flight gauge against gateway.max_in_flight_requests.
// Value = max(count/max*100) across live Redis samples (and the local gauge).
const OpsAlertMetricGatewayInFlightUsagePercent = "gateway_inflight_usage_percent"

const (
	gatewayInFlightKeyPrefix = "ops:gateway:inflight_relay:"
	gatewayInFlightKeyTTL    = 90 * time.Second
	gatewayInFlightScanCount = 32
)

// GatewayInFlightGauge reads the process-local relay gate.
// Wired from middleware via server.ProvideGatewayInFlightGauge so service does
// not import middleware (middleware already imports service).
type GatewayInFlightGauge interface {
	Count() int64
	Max() int
}

type gatewayInFlightSample struct {
	Count int64 `json:"count"`
	Max   int   `json:"max"`
	TS    int64 `json:"ts"`
}

// SetGatewayInFlightGauge attaches the process-local relay gauge used to
// publish this node's sample and as a local fallback when Redis is empty.
func (s *OpsAlertEvaluatorService) SetGatewayInFlightGauge(gauge GatewayInFlightGauge) {
	if s == nil {
		return
	}
	s.gatewayInFlightGauge = gauge
}

func (s *OpsAlertEvaluatorService) publishGatewayInFlight(ctx context.Context) {
	if s == nil || s.redisClient == nil || s.gatewayInFlightGauge == nil {
		return
	}
	max := s.gatewayInFlightGauge.Max()
	if max <= 0 {
		// Gate disabled: do not publish a 0/0 sample that would look like "idle".
		return
	}
	sample := gatewayInFlightSample{
		Count: s.gatewayInFlightGauge.Count(),
		Max:   max,
		TS:    time.Now().UTC().Unix(),
	}
	if sample.Count < 0 {
		sample.Count = 0
	}
	payload, err := json.Marshal(sample)
	if err != nil {
		return
	}
	key := gatewayInFlightKeyPrefix + strings.TrimSpace(s.instanceID)
	if key == gatewayInFlightKeyPrefix {
		return
	}
	_ = s.redisClient.Set(ctx, key, payload, gatewayInFlightKeyTTL).Err()
}

func (s *OpsAlertEvaluatorService) computeGatewayInFlightUsagePercent(ctx context.Context) (float64, bool) {
	if s == nil {
		return 0, false
	}

	maxPct := 0.0
	found := false

	if s.gatewayInFlightGauge != nil {
		if pct, ok := gatewayInFlightUsagePercent(s.gatewayInFlightGauge.Count(), s.gatewayInFlightGauge.Max()); ok {
			maxPct = pct
			found = true
		}
	}

	if s.redisClient != nil {
		var cursor uint64
		for {
			keys, next, err := s.redisClient.Scan(ctx, cursor, gatewayInFlightKeyPrefix+"*", gatewayInFlightScanCount).Result()
			if err != nil {
				break
			}
			for _, key := range keys {
				raw, err := s.redisClient.Get(ctx, key).Bytes()
				if err != nil {
					if err == redis.Nil {
						continue
					}
					continue
				}
				var sample gatewayInFlightSample
				if err := json.Unmarshal(raw, &sample); err != nil {
					continue
				}
				if pct, ok := gatewayInFlightUsagePercent(sample.Count, sample.Max); ok && (!found || pct > maxPct) {
					maxPct = pct
					found = true
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}

	if !found {
		return 0, false
	}
	return maxPct, true
}

func gatewayInFlightUsagePercent(count int64, max int) (float64, bool) {
	if max <= 0 {
		return 0, false
	}
	if count < 0 {
		count = 0
	}
	pct := (float64(count) / float64(max)) * 100
	if pct < 0 {
		pct = 0
	}
	return pct, true
}

func gatewayInFlightSampleKey(instanceID string) string {
	return gatewayInFlightKeyPrefix + strings.TrimSpace(instanceID)
}
