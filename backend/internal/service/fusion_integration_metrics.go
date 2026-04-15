package service

import (
	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// FusionIntegrationMetrics aggregates Tier1/Tier2 fusion counters for ops dashboards and alerts.
type FusionIntegrationMetrics struct {
	BridgeDispatchTotal    int64   `json:"bridge_dispatch_total"`
	BridgeDispatchErrors   int64   `json:"bridge_dispatch_errors"`
	AffinityLookups        int64   `json:"affinity_lookups"`
	AffinityHits           int64   `json:"affinity_hits"`
	AffinityHitRatio       float64 `json:"affinity_hit_ratio"`
	PaymentWebhookFailures int64   `json:"payment_webhook_failures"`
}

// CollectFusionIntegrationMetrics returns a point-in-time snapshot of fusion-related counters.
func CollectFusionIntegrationMetrics() FusionIntegrationMetrics {
	total, errs := BridgeDispatchStats()
	lookups, hits := newapifusion.AffinityHitStats()
	payf := FusionRuntimeFailureStats()
	ratio := 0.0
	if lookups > 0 {
		ratio = float64(hits) / float64(lookups)
	}
	return FusionIntegrationMetrics{
		BridgeDispatchTotal:    total,
		BridgeDispatchErrors:   errs,
		AffinityLookups:        lookups,
		AffinityHits:           hits,
		AffinityHitRatio:       ratio,
		PaymentWebhookFailures: payf,
	}
}
