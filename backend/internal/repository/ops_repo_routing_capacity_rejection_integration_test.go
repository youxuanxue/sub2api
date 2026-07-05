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
// also 429s with owner=client. error_phase='routing' is the persisted
// discriminator that makes a clean count possible without a new column.
//
// Routing-phase platform faults now count toward SLA error_count_sla (owner IN
// platform/provider) while client auth/request limits count only in the denominator.
func TestRoutingCapacityRejectionCountIsolatesRoutingPhase(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	insert := func(phase, owner, platform, requestedModel, model string, statusCode int) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code,
				error_owner, platform, requested_model, model, created_at
			) VALUES ($1, 'api_error', 'error', $2, $3, $4, $5, $6, $7)`,
			phase, statusCode, owner, platform, requestedModel, model, at,
		)
		require.NoError(t, err)
	}

	// Routing-phase capacity rejections — MUST be counted. Split across platforms
	// so the top-cause breakdown is exercised (anthropic 2 : openai 1).
	insert("routing", "platform", "anthropic", "claude-sonnet-4-5", "mapped-a", 429) // local empty pool fast-fail (#575)
	insert("routing", "platform", "anthropic", "claude-sonnet-4-5", "mapped-b", 503) // relayed downstream-capacity (rare 503 terminal)
	insert("routing", "platform", "openai", "", "gpt-5.1", 429)                      // a second platform's empty-pool rejection

	// NOT routing — must NOT be counted, even though some are 429:
	insert("auth", "client", "anthropic", "claude-sonnet-4-5", "", 429)     // user-level rate limit (their own quota)
	insert("upstream", "provider", "anthropic", "claude-opus-4-8", "", 429) // real provider rate_limit_error
	insert("request", "client", "openai", "gpt-5.1", "", 429)               // concurrency/queue client limit
	insert("internal", "platform", "gemini", "gemini-2.5-pro", "", 500)     // non-capacity platform error
	insert("upstream", "provider", "openai", "gpt-5.1", "", 502)            // ordinary provider failure

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
	// anthropic/openai rows above must NOT leak into these counts. These routing
	// rows carry NULL user_id, so they count toward each platform's total but
	// surface no nested user (Count includes NULL-user rows; Users excludes them).
	platforms, err := repo.TopRoutingCapacityRejectionByPlatform(ctx, filter, 2, 3)
	require.NoError(t, err)
	require.Len(t, platforms, 2)
	require.Equal(t, "anthropic", platforms[0].Platform)
	require.EqualValues(t, 2, platforms[0].Count)
	require.Empty(t, platforms[0].Users, "NULL-user routing rows count toward the total but surface no user")
	require.Equal(t, "openai", platforms[1].Platform)
	require.EqualValues(t, 1, platforms[1].Count)
	require.Empty(t, platforms[1].Users)

	models, err := repo.TopRoutingCapacityRejectionByModel(ctx, filter, 3)
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "claude-sonnet-4-5", models[0].Model, "requested_model wins over mapped model")
	require.EqualValues(t, 2, models[0].Count)
	require.Equal(t, "gpt-5.1", models[1].Model, "legacy rows fall back to model when requested_model is blank")
	require.EqualValues(t, 1, models[1].Count)

	// SLA error count = platform/provider final failures only (client auth/request rows stay in denominator).
	overview, err := repo.GetDashboardOverview(ctx, filter)
	require.NoError(t, err)
	require.NotNil(t, overview)
	require.EqualValues(t, 6, overview.ErrorCountSLA,
		"routing platform + provider/internal platform failures count toward SLA; client faults do not")
}

// TestTopRoutingCapacityRejectionByPlatform pins the JOINT breakdown that powers
// the P0 card's single 主因 line: per platform, the total rejection Count (ALL
// routing rows, including NULL-user) plus the top contributing users (user id +
// api-key NAME, resolved by join or deleted-key snapshot) over routing rows with
// user_id IS NOT NULL, ranked within each platform. The headline case is a user
// that spans TWO platforms (via two keys) — it must be attributed to BOTH pools
// with the right per-platform key name, which the old two marginal queries could
// not express. Non-routing rows are excluded; NULL-user rows count toward Count
// but never appear as a nested user. All names below are synthetic.
func TestTopRoutingCapacityRejectionByPlatform(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	// A real user with two live api_keys — one used on each platform, so the same
	// user surfaces under both pools with its respective key name (the cross-platform
	// case the joint view fixes). Cleaned up after (api_keys cascade on user delete).
	var liveUserID, keyAnthropicID, keyNewapiID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		"routing-byplatform-test@example.com").Scan(&liveUserID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", liveUserID)
	})
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys (user_id, key, name) VALUES ($1, $2, $3) RETURNING id`,
		liveUserID, "sk-routing-byplatform-a", "eval-harness").Scan(&keyAnthropicID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys (user_id, key, name) VALUES ($1, $2, $3) RETURNING id`,
		liveUserID, "sk-routing-byplatform-b", "ci-runner").Scan(&keyNewapiID))

	insert := func(phase, platform string, userID, apiKeyID *int64, deletedKeyName string) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code, error_owner,
				platform, user_id, api_key_id, deleted_key_name, created_at
			) VALUES ($1, 'api_error', 'error', 429, 'platform', $2, $3, $4, $5, $6)`,
			phase, platform, userID, apiKeyID, deletedKeyName, at)
		require.NoError(t, err)
	}
	i64 := func(v int64) *int64 { return &v }

	// anthropic: live user/eval-harness ×3, hard-deleted-key user 9001/snap-key ×1
	// (no api_keys row → snapshot fallback), plus one NULL-user routing row.
	insert("routing", "anthropic", i64(liveUserID), i64(keyAnthropicID), "")
	insert("routing", "anthropic", i64(liveUserID), i64(keyAnthropicID), "")
	insert("routing", "anthropic", i64(liveUserID), i64(keyAnthropicID), "")
	insert("routing", "anthropic", i64(9001), i64(987654321), "snap-key")
	insert("routing", "anthropic", nil, nil, "")
	// newapi: the SAME live user, its OTHER key — cross-platform attribution.
	insert("routing", "newapi", i64(liveUserID), i64(keyNewapiID), "")
	// Must NOT count: non-routing row for the live user.
	insert("auth", "anthropic", i64(liveUserID), i64(keyAnthropicID), "")

	filter := &service.OpsDashboardFilter{StartTime: windowStart, EndTime: windowEnd, QueryMode: service.OpsQueryModeRaw}
	platforms, err := repo.TopRoutingCapacityRejectionByPlatform(ctx, filter, 2, 3)
	require.NoError(t, err)
	require.Len(t, platforms, 2)

	// anthropic: total 5 (3 + 1 + 1 NULL-user); nested users exclude the NULL-user
	// row, ordered by count desc, names resolved (live join + deleted-key snapshot).
	require.Equal(t, "anthropic", platforms[0].Platform)
	require.EqualValues(t, 5, platforms[0].Count, "platform total includes the NULL-user routing row")
	require.Len(t, platforms[0].Users, 2, "NULL-user row contributes to Count but not to Users")
	require.Equal(t, liveUserID, platforms[0].Users[0].UserID)
	require.Equal(t, "eval-harness", platforms[0].Users[0].APIKeyName, "live key name via api_keys join")
	require.EqualValues(t, 3, platforms[0].Users[0].Count)
	require.EqualValues(t, 9001, platforms[0].Users[1].UserID)
	require.Equal(t, "snap-key", platforms[0].Users[1].APIKeyName, "hard-deleted key name from snapshot fallback")
	require.EqualValues(t, 1, platforms[0].Users[1].Count)

	// newapi: the same live user, its other key — proof the joint view attributes a
	// cross-platform user to BOTH pools with the right per-platform key, which the
	// old marginal lines smeared into one ambiguous number.
	require.Equal(t, "newapi", platforms[1].Platform)
	require.EqualValues(t, 1, platforms[1].Count)
	require.Len(t, platforms[1].Users, 1)
	require.Equal(t, liveUserID, platforms[1].Users[0].UserID)
	require.Equal(t, "ci-runner", platforms[1].Users[0].APIKeyName)
	require.EqualValues(t, 1, platforms[1].Users[0].Count)
}
