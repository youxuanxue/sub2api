//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserVisibleFailureCountAndBreakdown(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)
	suffix := time.Now().UnixNano()

	var groupID, userID, apiKeyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO groups (name) VALUES ($1) RETURNING id`,
		fmt.Sprintf("uvf-group-%d", suffix)).Scan(&groupID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`,
		fmt.Sprintf("uvf-%d@example.com", suffix)).Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO api_keys (user_id, key, name, routing_mode) VALUES ($1, $2, $3, 'universal') RETURNING id`,
		userID, fmt.Sprintf("sk-uvf-%d", suffix), "training-key").Scan(&apiKeyID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", groupID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)
	i64 := func(v int64) *int64 { return &v }

	insert := func(owner, phase, typ string, status, upstream int, user *int64, countTokens bool, msg string, finalMsg string) {
		var upstreamArg any
		if upstream > 0 {
			upstreamArg = upstream
		}
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				user_id, api_key_id, group_id, account_id,
				error_phase, error_type, severity, status_code, error_owner,
				platform, requested_model, upstream_status_code, upstream_error_message,
				is_count_tokens, created_at, error_message
			) VALUES ($1, $2, $3, 76, $4, $5, 'error', $6, $7, 'openai', 'gpt-5.5', $8, $9, $10, $11, $12)`,
			user, apiKeyID, groupID, phase, typ, status, owner, upstreamArg, msg, countTokens, at, finalMsg)
		require.NoError(t, err)
	}

	// P0 owner scope: provider/platform terminal failures attributable to a user.
	insert("provider", "upstream", "rate_limit_error", 429, 429, i64(userID), false, "Too many pending requests", "")
	insert("platform", "internal", "api_error", 499, 0, i64(userID), false, "client cancelled after platform stall", "")
	// Client owner scope: client-owned terminal failure, also attributable.
	insert("client", "request", "invalid_request_error", 400, 0, i64(userID), false, "prompt too long", "")

	// Exclusions: recovered-200, count_tokens probe, and unattributed failure.
	insert("provider", "upstream", "upstream_error", 200, 429, i64(userID), false, "recovered", "")
	insert("provider", "upstream", "rate_limit_error", 429, 429, i64(userID), true, "count tokens", "")
	insert("provider", "upstream", "upstream_error", 502, 502, nil, false, "no user", "")

	// Caller cancellation may be recorded as 499, a legacy transport status,
	// or in either message column. These stay in raw logs but leave the alert
	// count and all breakdown facets. Provider/platform rows above stay intact.
	insert("client", "request", "api_error", 499, 0, i64(userID), false, "", "")
	insert("client", "request", "api_error", 502, 0, i64(userID), false, "Post upstream: context canceled", "")
	insert("client", "request", "api_error", 503, 0, i64(userID), false, "", "Context Cancelled")
	insert("client", "request", "api_error", 502, 0, i64(userID), false, "transport error", "context canceled")
	insert("provider", "upstream", "api_error", 504, 0, i64(userID), false, "context deadline exceeded", "")
	insert("provider", "upstream", "api_error", 502, 0, i64(userID), false, "context canceled", "")

	filter := &service.OpsDashboardFilter{StartTime: windowStart, EndTime: windowEnd, QueryMode: service.OpsQueryModeRaw}

	systemCount, err := repo.CountUserVisibleFailures(ctx, filter, "system")
	require.NoError(t, err)
	require.EqualValues(t, 4, systemCount, "provider/platform terminal failures count; recovered/count_tokens/unattributed/client rows do not")

	clientCount, err := repo.CountUserVisibleFailures(ctx, filter, "client")
	require.NoError(t, err)
	require.EqualValues(t, 1, clientCount, "ordinary client validation failures remain in the client scope")

	clientBreakdown, err := repo.GetUserVisibleFailureBreakdown(ctx, filter, "client", 3)
	require.NoError(t, err)
	require.EqualValues(t, 1, clientBreakdown.Failures)
	require.Len(t, clientBreakdown.Users, 1)
	require.EqualValues(t, 1, clientBreakdown.Users[0].Count)
	require.Len(t, clientBreakdown.Surfaces, 1)
	require.Equal(t, 400, clientBreakdown.Surfaces[0].StatusCode)
	require.Len(t, clientBreakdown.Roots, 1)
	require.EqualValues(t, 1, clientBreakdown.Roots[0].Count)
	var rawClientCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT count(*) FROM ops_error_logs WHERE error_owner = 'client' AND user_id = $1`, userID).Scan(&rawClientCount))
	require.Equal(t, 5, rawClientCount, "cancellations remain available in raw error logs")

	breakdown, err := repo.GetUserVisibleFailureBreakdown(ctx, filter, "system", 3)
	require.NoError(t, err)
	require.NotNil(t, breakdown)
	require.EqualValues(t, 4, breakdown.Failures)
	require.NotEmpty(t, breakdown.Users)
	require.Equal(t, userID, breakdown.Users[0].UserID)
	require.Equal(t, "training-key", breakdown.Users[0].APIKeyName)
	require.Equal(t, service.RoutingModeUniversal, breakdown.Users[0].APIKeyRoutingMode)
	require.NotEmpty(t, breakdown.Surfaces)
	require.Equal(t, 429, breakdown.Surfaces[0].StatusCode)
	require.Equal(t, 429, breakdown.Surfaces[0].UpstreamStatusCode)
	require.NotEmpty(t, breakdown.Roots)
	foundProviderUpstream := false
	for _, root := range breakdown.Roots {
		if root != nil && root.Phase == "upstream" && root.Owner == "provider" {
			foundProviderUpstream = true
			break
		}
	}
	require.True(t, foundProviderUpstream, "breakdown roots should include the provider/upstream failure")
}
