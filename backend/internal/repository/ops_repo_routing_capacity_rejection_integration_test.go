//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestRoutingCapacityRejectionCountIsolatesRoutingPhase pins the empty-pool-429
// alert blind-spot fix at the SQL layer: RoutingCapacityRejectionCount counts
// ONLY ops_error_logs rows with error_phase='routing' (the local empty-pool
// fast-fail 429 + relayed mirror-edge downstream-capacity rejections), and never
// user-level rate limits (phase upstream/request/auth) — even though those are
// also 429s and also is_business_limited. error_phase='routing' is the persisted
// discriminator that makes a clean count possible without a new column.
//
// It also confirms these rows are simultaneously EXCLUDED from the ratio metrics
// (error_rate/upstream_error_rate via NOT is_business_limited), which is exactly
// why a dedicated count is required: without it an empty-pool storm is invisible.
func TestRoutingCapacityRejectionCountIsolatesRoutingPhase(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	insert := func(phase, owner, platform string, statusCode int, businessLimited bool) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code,
				error_owner, platform, is_business_limited, created_at
			) VALUES ($1, 'api_error', 'error', $2, $3, $4, $5, $6)`,
			phase, statusCode, owner, platform, businessLimited, at,
		)
		require.NoError(t, err)
	}

	// Routing-phase capacity rejections — MUST be counted. Split across platforms
	// so the top-cause breakdown is exercised (anthropic 2 : openai 1).
	insert("routing", "platform", "anthropic", 429, true) // local empty pool fast-fail (#575)
	insert("routing", "platform", "anthropic", 503, true) // relayed downstream-capacity (rare 503 terminal)
	insert("routing", "platform", "openai", 429, true)    // a second platform's empty-pool rejection

	// NOT routing — must NOT be counted, even though some are 429 + business-limited:
	insert("auth", "client", "anthropic", 429, true)      // user-level rate limit (their own quota)
	insert("upstream", "provider", "anthropic", 429, false) // real provider rate_limit_error
	insert("request", "client", "openai", 429, true)        // concurrency/queue business limit
	insert("internal", "platform", "gemini", 500, false)    // non-capacity platform error
	insert("upstream", "provider", "openai", 502, false)    // ordinary provider failure

	filter := &service.OpsDashboardFilter{
		StartTime: windowStart,
		EndTime:   windowEnd,
		QueryMode: service.OpsQueryModeRaw,
	}

	// The metric query the alert evaluator drives: only error_phase='routing'.
	got, err := repo.CountRoutingCapacityRejections(ctx, filter)
	require.NoError(t, err)
	require.EqualValues(t, 3, got,
		"only error_phase='routing' rows count (2x anthropic + 1x openai); user-limit/provider/internal 429s are excluded")

	// Self-diagnosing 主因 breakdown: only routing rows, grouped by platform,
	// descending — so the P0 card names which pool is empty. The non-routing
	// anthropic/openai rows above must NOT leak into these counts.
	causes, err := repo.TopRoutingCapacityRejectionCauses(ctx, filter, 2)
	require.NoError(t, err)
	require.Len(t, causes, 2)
	require.Equal(t, "anthropic", causes[0].Platform)
	require.EqualValues(t, 2, causes[0].Count)
	require.Equal(t, "openai", causes[1].Platform)
	require.EqualValues(t, 1, causes[1].Count)

	// Cross-check the blind spot: the three routing rows are business-limited →
	// excluded from the ratio metrics' numerator AND denominator, which is exactly
	// why the dedicated count is required. SLA error count = only the
	// NOT-business-limited final failures: provider 429, internal 500, provider 502 = 3.
	overview, err := repo.GetDashboardOverview(ctx, filter)
	require.NoError(t, err)
	require.NotNil(t, overview)
	require.EqualValues(t, 3, overview.ErrorCountSLA,
		"business-limited routing/auth/request rows are excluded from the SLA error count")
}
