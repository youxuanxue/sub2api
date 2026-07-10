//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSeededAlertRuleStateAfterMigrations pins the enabled-alert-rule contract
// after all migrations apply: tk_060 seeds user-visible failure P0/P1, tk_061
// retires routing_capacity_rejection_count (replaced by user_visible_failure_count),
// and tk_036 latency rules ship disabled — every enabled rule has a working path.
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

	type seededRule struct {
		enabled       bool
		threshold     float64
		windowMinutes int
	}
	ruleFor := func(metricType string) seededRule {
		var rule seededRule
		err := integrationDB.QueryRowContext(ctx,
			`SELECT enabled, threshold, window_minutes FROM ops_alert_rules WHERE metric_type = $1 ORDER BY id LIMIT 1`,
			metricType,
		).Scan(&rule.enabled, &rule.threshold, &rule.windowMinutes)
		require.NoError(t, err, "metric_type %s should be seeded", metricType)
		return rule
	}

	absentFor := func(metricType string) {
		var id int64
		err := integrationDB.QueryRowContext(ctx,
			`SELECT id FROM ops_alert_rules WHERE metric_type = $1 ORDER BY id LIMIT 1`,
			metricType,
		).Scan(&id)
		require.Error(t, err, "metric_type %s should be removed", metricType)
	}

	// tk_060: user-experience-first P0/P1 guardrails.
	userVisibleFailureRule := ruleFor("user_visible_failure_count")
	require.True(t, userVisibleFailureRule.enabled,
		"tk_060 user_visible_failure_count rule must be enabled")
	require.Equal(t, 50.0, userVisibleFailureRule.threshold,
		"tk_064 raises the prod P0 user-visible threshold to 50 failures")
	require.Equal(t, 5, userVisibleFailureRule.windowMinutes,
		"prod P0 user-visible threshold is evaluated over 5 minutes")
	require.True(t, enabledFor("client_visible_failure_count"),
		"tk_060 client_visible_failure_count rule must be enabled")

	// tk_061: routing-capacity P0 retired to avoid double-paging the same incident.
	absentFor("routing_capacity_rejection_count")

	// tk_036: the unimplemented latency rules are disabled — they never fired
	// (no evaluator case) and their full-request-duration thresholds (2000/3000ms)
	// are wrong for an LLM streaming gateway, so wiring them would only add noise.
	require.False(t, enabledFor("p95_latency_ms"), "p95_latency_ms must be disabled (tk_036)")
	require.False(t, enabledFor("p99_latency_ms"), "p99_latency_ms must be disabled (tk_036)")
}
