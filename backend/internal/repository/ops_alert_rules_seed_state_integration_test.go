//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSeededAlertRuleStateAfterMigrations pins the enabled-alert-rule contract
// after all migrations apply: the retired routing-capacity P0 (tk_061) is gone,
// and the never-wired latency rules (tk_036) ship disabled — so every enabled
// rule has a working evaluator path (no silent dead rules).
func TestSeededAlertRuleStateAfterMigrations(t *testing.T) {
	ctx := context.Background()

	enabledFor := func(metricType string) bool {
		var enabled bool
		err := integrationDB.QueryRowContext(ctx,
			`SELECT enabled FROM ops_alert_rules WHERE metric_type = $1 ORDER BY id LIMIT 1`,
			metricType,
		).Scan(&enabled)
		require.NoError(t, err, "metric_type %s should be seeded", metricType)
		return enabled
	}
	existsFor := func(metricType string) bool {
		var exists bool
		err := integrationDB.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM ops_alert_rules WHERE metric_type = $1)`,
			metricType,
		).Scan(&exists)
		require.NoError(t, err, "metric_type %s existence check should query", metricType)
		return exists
	}

	// tk_061: the routing-capacity-rejection P0 is retired because
	// user_visible_failure_count is now the single experience-first P0.
	require.False(t, existsFor("routing_capacity_rejection_count"),
		"tk_061 must remove routing_capacity_rejection_count to avoid duplicate P0 pages")

	// tk_036: the unimplemented latency rules are disabled — they never fired
	// (no evaluator case) and their full-request-duration thresholds (2000/3000ms)
	// are wrong for an LLM streaming gateway, so wiring them would only add noise.
	require.False(t, enabledFor("p95_latency_ms"), "p95_latency_ms must be disabled (tk_036)")
	require.False(t, enabledFor("p99_latency_ms"), "p99_latency_ms must be disabled (tk_036)")
}
