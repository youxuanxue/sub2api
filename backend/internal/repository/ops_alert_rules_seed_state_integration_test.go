//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSeededAlertRuleStateAfterMigrations pins the enabled-alert-rule contract
// after all migrations apply: the routing-capacity P0 (tk_035) ships enabled,
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

	// tk_035: the routing-capacity-rejection P0 is on by default (this PR's core).
	require.True(t, enabledFor("routing_capacity_rejection_count"),
		"tk_035 routing_capacity_rejection_count rule must be enabled")

	// tk_036: the unimplemented latency rules are disabled — they never fired
	// (no evaluator case) and their full-request-duration thresholds (2000/3000ms)
	// are wrong for an LLM streaming gateway, so wiring them would only add noise.
	require.False(t, enabledFor("p95_latency_ms"), "p95_latency_ms must be disabled (tk_036)")
	require.False(t, enabledFor("p99_latency_ms"), "p99_latency_ms must be disabled (tk_036)")
}
