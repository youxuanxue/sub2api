package handler

import (
	"fmt"
	"net/http"
	"sort"
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
	writeLabeledSeriesHeader := func(name, help, typ string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, typ)
	}
	renderLabels := func(labels map[string]string) string {
		if len(labels) == 0 {
			return ""
		}
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf(`%s=%q`, key, labels[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	writeCounter("sub2api_bridge_dispatch_total", "New API adaptor bridge dispatch attempts.", m.BridgeDispatchTotal)
	writeCounter("sub2api_bridge_dispatch_errors", "New API adaptor bridge dispatch errors.", m.BridgeDispatchErrors)
	writeCounter("sub2api_affinity_lookups", "Affinity lookup attempts.", m.AffinityLookups)
	writeCounter("sub2api_affinity_hits", "Affinity cache hits.", m.AffinityHits)
	writeGauge("sub2api_affinity_hit_ratio", "Affinity hit ratio (0..1).", m.AffinityHitRatio)
	writeCounter("sub2api_payment_webhook_failures", "Payment webhook verification/processing failures (cumulative).", m.PaymentWebhookFailures)

	writeLabeledSeriesHeader("sub2api_http_requests_total", "HTTP requests by platform/model/status class.", "counter")
	for _, metric := range m.HTTPRequests {
		fmt.Fprintf(&b, "sub2api_http_requests_total%s %g\n", renderLabels(metric.Labels), metric.Value)
	}

	writeLabeledSeriesHeader("sub2api_http_request_duration_seconds", "HTTP request duration histogram.", "histogram")
	for _, metric := range m.HTTPRequestDurations {
		baseLabels := metric.Labels
		for _, bucket := range metric.Buckets {
			labels := cloneMetricLabels(baseLabels)
			labels["le"] = fmt.Sprintf("%g", bucket.UpperBound)
			fmt.Fprintf(&b, "sub2api_http_request_duration_seconds_bucket%s %d\n", renderLabels(labels), bucket.Count)
		}
		infLabels := cloneMetricLabels(baseLabels)
		infLabels["le"] = "+Inf"
		fmt.Fprintf(&b, "sub2api_http_request_duration_seconds_bucket%s %d\n", renderLabels(infLabels), metric.Count)
		fmt.Fprintf(&b, "sub2api_http_request_duration_seconds_sum%s %g\n", renderLabels(baseLabels), metric.Sum)
		fmt.Fprintf(&b, "sub2api_http_request_duration_seconds_count%s %d\n", renderLabels(baseLabels), metric.Count)
	}

	writeLabeledSeriesHeader("sub2api_first_token_seconds", "Time to first token histogram.", "histogram")
	for _, metric := range m.FirstTokenDurations {
		baseLabels := metric.Labels
		for _, bucket := range metric.Buckets {
			labels := cloneMetricLabels(baseLabels)
			labels["le"] = fmt.Sprintf("%g", bucket.UpperBound)
			fmt.Fprintf(&b, "sub2api_first_token_seconds_bucket%s %d\n", renderLabels(labels), bucket.Count)
		}
		infLabels := cloneMetricLabels(baseLabels)
		infLabels["le"] = "+Inf"
		fmt.Fprintf(&b, "sub2api_first_token_seconds_bucket%s %d\n", renderLabels(infLabels), metric.Count)
		fmt.Fprintf(&b, "sub2api_first_token_seconds_sum%s %g\n", renderLabels(baseLabels), metric.Sum)
		fmt.Fprintf(&b, "sub2api_first_token_seconds_count%s %d\n", renderLabels(baseLabels), metric.Count)
	}

	writeLabeledSeriesHeader("sub2api_account_pool_size", "Account pool size by platform/status.", "gauge")
	for _, metric := range m.AccountPoolSizes {
		fmt.Fprintf(&b, "sub2api_account_pool_size%s %g\n", renderLabels(metric.Labels), metric.Value)
	}

	writeLabeledSeriesHeader("sub2api_account_failure_total", "Account failures by platform/account/reason.", "counter")
	for _, metric := range m.AccountFailures {
		fmt.Fprintf(&b, "sub2api_account_failure_total%s %g\n", renderLabels(metric.Labels), metric.Value)
	}

	writeLabeledSeriesHeader("sub2api_usage_billing_apply_errors_total", "Usage billing apply errors by reason.", "counter")
	for _, metric := range m.UsageBillingApplyErrors {
		fmt.Fprintf(&b, "sub2api_usage_billing_apply_errors_total%s %g\n", renderLabels(metric.Labels), metric.Value)
	}
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(b.String()))
}

func cloneMetricLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(labels))
	for key, value := range labels {
		out[key] = value
	}
	return out
}
