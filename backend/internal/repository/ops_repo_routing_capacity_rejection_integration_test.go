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

// TestTopRoutingCapacityRejectionUsers pins the WHO breakdown that names the
// rejected users on the P0 card: grouped by (user_id, api_key_id) over routing
// rows only, ordered by count, with the api-key NAME resolved via join (live key)
// or the deleted-key snapshot (hard-deleted key). Unattributable rows (NULL
// user_id) and non-routing rows are excluded.
func TestTopRoutingCapacityRejectionUsers(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	// A real user + live api_key, for the join (live-name) path. Cleaned up after
	// (api_keys cascades on user delete) so the shared integration DB stays tidy.
	var liveUserID, liveKeyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		"routing-users-test@example.com").Scan(&liveUserID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", liveUserID)
	})
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys (user_id, key, name) VALUES ($1, $2, $3) RETURNING id`,
		liveUserID, "sk-routing-users-test", "eval-harness").Scan(&liveKeyID))

	insert := func(phase string, userID, apiKeyID *int64, deletedKeyName string) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code, error_owner,
				user_id, api_key_id, deleted_key_name, is_business_limited, created_at
			) VALUES ($1, 'api_error', 'error', 429, 'platform', $2, $3, $4, true, $5)`,
			phase, userID, apiKeyID, deletedKeyName, at)
		require.NoError(t, err)
	}
	i64 := func(v int64) *int64 { return &v }

	// Live user/key — 2 routing rejections → name resolved from api_keys join.
	insert("routing", i64(liveUserID), i64(liveKeyID), "")
	insert("routing", i64(liveUserID), i64(liveKeyID), "")
	// Hard-deleted key — 1 routing rejection, api_key_id has no api_keys row →
	// name from the deleted-key snapshot.
	insert("routing", i64(9001), i64(987654321), "snap-key")
	// Must NOT count: non-routing row for the live user.
	insert("auth", i64(liveUserID), i64(liveKeyID), "")
	// Must NOT count: routing row with NULL user_id (unattributable).
	insert("routing", nil, nil, "")

	filter := &service.OpsDashboardFilter{StartTime: windowStart, EndTime: windowEnd, QueryMode: service.OpsQueryModeRaw}
	users, err := repo.TopRoutingCapacityRejectionUsers(ctx, filter, 3)
	require.NoError(t, err)
	require.Len(t, users, 2, "two attributable users; NULL-user and non-routing rows excluded")

	// Ordered by count desc: live user (2) then snapshot user (1).
	require.Equal(t, liveUserID, users[0].UserID)
	require.Equal(t, "eval-harness", users[0].APIKeyName, "live key name resolved via api_keys join")
	require.EqualValues(t, 2, users[0].Count)
	require.EqualValues(t, 9001, users[1].UserID)
	require.Equal(t, "snap-key", users[1].APIKeyName, "hard-deleted key name from snapshot fallback")
	require.EqualValues(t, 1, users[1].Count)
}
