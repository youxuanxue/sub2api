package service

import (
	"sort"
	"strings"
	"sync"
	"time"

	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// FusionLabeledMetric stores one labeled counter/gauge sample.
type FusionLabeledMetric struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// FusionHistogramBucket stores one cumulative Prometheus bucket.
type FusionHistogramBucket struct {
	UpperBound float64 `json:"upper_bound"`
	Count      uint64  `json:"count"`
}

// FusionHistogramMetric stores a histogram snapshot with Prometheus-style buckets.
type FusionHistogramMetric struct {
	Labels  map[string]string       `json:"labels"`
	Count   uint64                  `json:"count"`
	Sum     float64                 `json:"sum"`
	Buckets []FusionHistogramBucket `json:"buckets"`
}

// FusionIntegrationMetrics aggregates Tier1/Tier2 fusion counters for ops dashboards and alerts.
type FusionIntegrationMetrics struct {
	BridgeDispatchTotal    int64   `json:"bridge_dispatch_total"`
	BridgeDispatchErrors   int64   `json:"bridge_dispatch_errors"`
	AffinityLookups        int64   `json:"affinity_lookups"`
	AffinityHits           int64   `json:"affinity_hits"`
	AffinityHitRatio       float64 `json:"affinity_hit_ratio"`
	PaymentWebhookFailures int64   `json:"payment_webhook_failures"`

	HTTPRequests            []FusionLabeledMetric   `json:"http_requests"`
	HTTPRequestDurations    []FusionHistogramMetric `json:"http_request_durations"`
	FirstTokenDurations     []FusionHistogramMetric `json:"first_token_durations"`
	AccountPoolSizes        []FusionLabeledMetric   `json:"account_pool_sizes"`
	AccountFailures         []FusionLabeledMetric   `json:"account_failures"`
	UsageBillingApplyErrors []FusionLabeledMetric   `json:"usage_billing_apply_errors"`
}

type fusionMetricSeriesKey struct {
	A string
	B string
	C string
}

type fusionHistogramState struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

type fusionMetricsRegistry struct {
	mu sync.RWMutex

	httpRequests            map[fusionMetricSeriesKey]uint64
	httpRequestDurations    map[fusionMetricSeriesKey]*fusionHistogramState
	firstTokenDurations     map[fusionMetricSeriesKey]*fusionHistogramState
	accountPoolSizes        map[fusionMetricSeriesKey]float64
	accountFailures         map[fusionMetricSeriesKey]uint64
	usageBillingApplyErrors map[fusionMetricSeriesKey]uint64
}

var (
	fusionHistogramBounds = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60}
	fusionMetrics         = &fusionMetricsRegistry{
		httpRequests:            make(map[fusionMetricSeriesKey]uint64),
		httpRequestDurations:    make(map[fusionMetricSeriesKey]*fusionHistogramState),
		firstTokenDurations:     make(map[fusionMetricSeriesKey]*fusionHistogramState),
		accountPoolSizes:        make(map[fusionMetricSeriesKey]float64),
		accountFailures:         make(map[fusionMetricSeriesKey]uint64),
		usageBillingApplyErrors: make(map[fusionMetricSeriesKey]uint64),
	}
	fusionOpenAISchedulerMetricsProviderMu sync.RWMutex
	fusionOpenAISchedulerMetricsProvider   func() OpenAIAccountSchedulerMetricsSnapshot
)

func RegisterFusionOpenAISchedulerMetricsProvider(provider func() OpenAIAccountSchedulerMetricsSnapshot) {
	fusionOpenAISchedulerMetricsProviderMu.Lock()
	defer fusionOpenAISchedulerMetricsProviderMu.Unlock()
	fusionOpenAISchedulerMetricsProvider = provider
}

func ObserveFusionHTTPRequest(platform, model string, statusCode int, duration time.Duration, firstTokenMs *int64) {
	platform = normalizeFusionMetricLabel(platform)
	model = normalizeFusionMetricLabel(model)
	statusClass := normalizeFusionStatusClass(statusCode)

	fusionMetrics.mu.Lock()
	defer fusionMetrics.mu.Unlock()

	fusionMetrics.httpRequests[fusionMetricSeriesKey{A: platform, B: model, C: statusClass}]++
	observeFusionHistogram(fusionMetrics.httpRequestDurations, fusionMetricSeriesKey{A: platform, B: model}, duration.Seconds())
	if firstTokenMs != nil && *firstTokenMs > 0 {
		observeFusionHistogram(fusionMetrics.firstTokenDurations, fusionMetricSeriesKey{A: platform, B: model}, float64(*firstTokenMs)/1000.0)
	}
}

func RecordFusionAccountFailure(platform string, accountID int64, reason string) {
	if accountID <= 0 {
		return
	}
	platform = normalizeFusionMetricLabel(platform)
	reason = normalizeFusionMetricLabel(reason)

	fusionMetrics.mu.Lock()
	defer fusionMetrics.mu.Unlock()
	fusionMetrics.accountFailures[fusionMetricSeriesKey{
		A: platform,
		B: normalizeFusionMetricLabel(int64ToString(accountID)),
		C: reason,
	}]++
}

func SetFusionAccountPoolSize(platform, status string, count int) {
	platform = normalizeFusionMetricLabel(platform)
	status = normalizeFusionMetricLabel(status)

	fusionMetrics.mu.Lock()
	defer fusionMetrics.mu.Unlock()
	fusionMetrics.accountPoolSizes[fusionMetricSeriesKey{A: platform, B: status}] = float64(count)
}

func RecordFusionUsageBillingApplyError(reason string) {
	reason = normalizeFusionMetricLabel(reason)

	fusionMetrics.mu.Lock()
	defer fusionMetrics.mu.Unlock()
	fusionMetrics.usageBillingApplyErrors[fusionMetricSeriesKey{A: reason}]++
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

	fusionMetrics.mu.RLock()
	defer fusionMetrics.mu.RUnlock()

	metrics := FusionIntegrationMetrics{
		BridgeDispatchTotal:     total,
		BridgeDispatchErrors:    errs,
		AffinityLookups:         lookups,
		AffinityHits:            hits,
		AffinityHitRatio:        ratio,
		PaymentWebhookFailures:  payf,
		HTTPRequests:            snapshotFusionCounters(fusionMetrics.httpRequests, []string{"platform", "model", "status_class"}),
		HTTPRequestDurations:    snapshotFusionHistograms(fusionMetrics.httpRequestDurations, []string{"platform", "model"}),
		FirstTokenDurations:     snapshotFusionHistograms(fusionMetrics.firstTokenDurations, []string{"platform", "model"}),
		AccountPoolSizes:        snapshotFusionGauges(fusionMetrics.accountPoolSizes, []string{"platform", "status"}),
		AccountFailures:         snapshotFusionCounters(fusionMetrics.accountFailures, []string{"platform", "account_id", "reason"}),
		UsageBillingApplyErrors: snapshotFusionCounters(fusionMetrics.usageBillingApplyErrors, []string{"reason"}),
	}

	if scheduler := snapshotFusionOpenAISchedulerMetrics(); scheduler.RuntimeStatsAccountCount > 0 {
		metrics.AccountPoolSizes = append(metrics.AccountPoolSizes, FusionLabeledMetric{
			Labels: map[string]string{"platform": "openai", "status": "active"},
			Value:  float64(scheduler.RuntimeStatsAccountCount),
		})
	}
	sortFusionLabeledMetrics(metrics.AccountPoolSizes)

	return metrics
}

func snapshotFusionOpenAISchedulerMetrics() OpenAIAccountSchedulerMetricsSnapshot {
	fusionOpenAISchedulerMetricsProviderMu.RLock()
	defer fusionOpenAISchedulerMetricsProviderMu.RUnlock()
	if fusionOpenAISchedulerMetricsProvider == nil {
		return OpenAIAccountSchedulerMetricsSnapshot{}
	}
	return fusionOpenAISchedulerMetricsProvider()
}

func observeFusionHistogram(dst map[fusionMetricSeriesKey]*fusionHistogramState, key fusionMetricSeriesKey, value float64) {
	state := dst[key]
	if state == nil {
		state = &fusionHistogramState{Buckets: make([]uint64, len(fusionHistogramBounds))}
		dst[key] = state
	}
	state.Count++
	state.Sum += value
	for i, upper := range fusionHistogramBounds {
		if value <= upper {
			state.Buckets[i]++
		}
	}
}

func snapshotFusionCounters(src map[fusionMetricSeriesKey]uint64, labelNames []string) []FusionLabeledMetric {
	out := make([]FusionLabeledMetric, 0, len(src))
	for key, value := range src {
		out = append(out, FusionLabeledMetric{
			Labels: fusionMetricLabels(key, labelNames),
			Value:  float64(value),
		})
	}
	sortFusionLabeledMetrics(out)
	return out
}

func snapshotFusionGauges(src map[fusionMetricSeriesKey]float64, labelNames []string) []FusionLabeledMetric {
	out := make([]FusionLabeledMetric, 0, len(src))
	for key, value := range src {
		out = append(out, FusionLabeledMetric{
			Labels: fusionMetricLabels(key, labelNames),
			Value:  value,
		})
	}
	sortFusionLabeledMetrics(out)
	return out
}

func snapshotFusionHistograms(src map[fusionMetricSeriesKey]*fusionHistogramState, labelNames []string) []FusionHistogramMetric {
	out := make([]FusionHistogramMetric, 0, len(src))
	for key, state := range src {
		if state == nil {
			continue
		}
		buckets := make([]FusionHistogramBucket, 0, len(fusionHistogramBounds))
		for i, upper := range fusionHistogramBounds {
			buckets = append(buckets, FusionHistogramBucket{
				UpperBound: upper,
				// state.Buckets is already cumulative (observeFusionHistogram increments all <= upper bounds).
				Count: state.Buckets[i],
			})
		}
		out = append(out, FusionHistogramMetric{
			Labels:  fusionMetricLabels(key, labelNames),
			Count:   state.Count,
			Sum:     state.Sum,
			Buckets: buckets,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return fusionLabelsSortKey(out[i].Labels) < fusionLabelsSortKey(out[j].Labels)
	})
	return out
}

func fusionMetricLabels(key fusionMetricSeriesKey, names []string) map[string]string {
	values := []string{key.A, key.B, key.C}
	out := make(map[string]string, len(names))
	for i, name := range names {
		if i >= len(values) {
			break
		}
		out[name] = values[i]
	}
	return out
}

func sortFusionLabeledMetrics(items []FusionLabeledMetric) {
	sort.Slice(items, func(i, j int) bool {
		return fusionLabelsSortKey(items[i].Labels) < fusionLabelsSortKey(items[j].Labels)
	})
}

func fusionLabelsSortKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(labels[key])
		b.WriteByte(';')
	}
	return b.String()
}

func normalizeFusionMetricLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func normalizeFusionStatusClass(statusCode int) string {
	if statusCode <= 0 {
		return "unknown"
	}
	return int64ToString(int64(statusCode/100)) + "xx"
}
