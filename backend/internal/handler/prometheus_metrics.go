package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// PrometheusMetrics exposes process-local fusion counters in Prometheus text format (no external registry dependency).
// GET /metrics
func PrometheusMetrics(c *gin.Context) {
	m := service.CollectFusionIntegrationMetrics()
	var b strings.Builder
	writeCounter := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&b, "%s %d\n", name, v)
	}
	writeGauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&b, "%s %g\n", name, v)
	}
	writeCounter("sub2api_bridge_dispatch_total", "New API adaptor bridge dispatch attempts.", m.BridgeDispatchTotal)
	writeCounter("sub2api_bridge_dispatch_errors", "New API adaptor bridge dispatch errors.", m.BridgeDispatchErrors)
	writeCounter("sub2api_affinity_lookups", "Affinity lookup attempts.", m.AffinityLookups)
	writeCounter("sub2api_affinity_hits", "Affinity cache hits.", m.AffinityHits)
	writeGauge("sub2api_affinity_hit_ratio", "Affinity hit ratio (0..1).", m.AffinityHitRatio)
	writeCounter("sub2api_payment_webhook_failures", "Payment webhook verification/processing failures (cumulative).", m.PaymentWebhookFailures)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}
