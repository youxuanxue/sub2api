package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
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
	Count int64  `json:"count"`
	Max   int    `json:"max"`
	TS    int64  `json:"ts"`
	Node  string `json:"node,omitempty"`
}

type gatewayInFlightReading struct {
	Percent  float64
	HotLabel string
}

// SetGatewayInFlightGauge attaches the process-local relay gauge used to
// publish this node's sample and as a local fallback when Redis is empty.
func (s *OpsAlertEvaluatorService) SetGatewayInFlightGauge(gauge GatewayInFlightGauge) {
	if s == nil {
		return
	}
	s.gatewayInFlightGauge = gauge
}

func gatewayInFlightNodeName() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(host)
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
		Node:  gatewayInFlightNodeName(),
	}
	if sample.Count < 0 {
		sample.Count = 0
	}
	payload, err := json.Marshal(sample)
	if err != nil {
		return
	}
	key := gatewayInFlightSampleKey(s.instanceID)
	if key == gatewayInFlightKeyPrefix {
		return
	}
	_ = s.redisClient.Set(ctx, key, payload, gatewayInFlightKeyTTL).Err()
}

func (s *OpsAlertEvaluatorService) computeGatewayInFlightUsagePercent(ctx context.Context) (float64, bool) {
	reading, ok := s.readGatewayInFlightUsage(ctx)
	if !ok {
		return 0, false
	}
	return reading.Percent, true
}

func (s *OpsAlertEvaluatorService) gatewayInFlightHotCause(ctx context.Context) string {
	reading, ok := s.readGatewayInFlightUsage(ctx)
	if !ok {
		return ""
	}
	return strings.TrimSpace(reading.HotLabel)
}

func (s *OpsAlertEvaluatorService) readGatewayInFlightUsage(ctx context.Context) (gatewayInFlightReading, bool) {
	if s == nil {
		return gatewayInFlightReading{}, false
	}

	var best gatewayInFlightReading
	found := false

	consider := func(node string, count int64, max int) {
		pct, ok := gatewayInFlightUsagePercent(count, max)
		if !ok {
			return
		}
		label := formatGatewayInFlightHotLabel(node, count, max, pct)
		if !found || pct > best.Percent {
			best = gatewayInFlightReading{Percent: pct, HotLabel: label}
			found = true
		}
	}

	if s.gatewayInFlightGauge != nil {
		consider(gatewayInFlightNodeName(), s.gatewayInFlightGauge.Count(), s.gatewayInFlightGauge.Max())
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
					continue
				}
				var sample gatewayInFlightSample
				if err := json.Unmarshal(raw, &sample); err != nil {
					continue
				}
				node := strings.TrimSpace(sample.Node)
				if node == "" {
					node = strings.TrimPrefix(key, gatewayInFlightKeyPrefix)
				}
				consider(node, sample.Count, sample.Max)
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}

	if !found {
		return gatewayInFlightReading{}, false
	}
	return best, true
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

func formatGatewayInFlightHotLabel(node string, count int64, max int, pct float64) string {
	node = strings.TrimSpace(node)
	if node == "" {
		node = "unknown-node"
	}
	return fmt.Sprintf("%s · in-flight %d/%d (%.1f%%)", node, count, max, pct)
}

func gatewayInFlightSampleKey(instanceID string) string {
	return gatewayInFlightKeyPrefix + strings.TrimSpace(instanceID)
}
