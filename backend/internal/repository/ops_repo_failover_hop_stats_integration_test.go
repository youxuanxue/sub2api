//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestGetFailoverHopStats verifies the per-account wasted-failover-hop KPI:
//   - only recovered-200 rows (client status < 400 with upstream attempts) count;
//   - jsonb_array_length(upstream_errors) is the wasted-attempt count (no minus-1);
//   - failover_hops counts only account-switch kinds (split_part(kind,':',1));
//   - scope is openai/newapi only;
//   - aggregation is per (account, platform).
func TestGetFailoverHopStats(t *testing.T) {
	ctx := context.Background()
	_, _ = integrationDB.ExecContext(ctx, "TRUNCATE ops_error_logs RESTART IDENTITY CASCADE")

	repo := NewOpsRepository(integrationDB).(*opsRepository)

	windowStart := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	windowEnd := windowStart.Add(time.Hour)
	at := windowStart.Add(5 * time.Minute)

	insert := func(statusCode int, accountID int64, platform, upstreamErrors string) {
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO ops_error_logs (
				error_phase, error_type, severity, status_code, error_owner,
				account_id, platform, upstream_errors, is_count_tokens, created_at
			) VALUES ('upstream', 'upstream_error', 'error', $1, 'provider',
				$2, $3, $4::jsonb, FALSE, $5)`,
			statusCode, accountID, platform, upstreamErrors, at,
		)
		require.NoError(t, err)
	}

	// account 100 (openai): two recovered-200 requests.
	//   row A: 2 failover events + 1 retry  -> failover_hops=2, wasted_attempts=3
	insert(200, 100, "openai", `[{"kind":"failover"},{"kind":"failover_on_400"},{"kind":"retry"}]`)
	//   row B: 1 failover                   -> failover_hops=1, wasted_attempts=1
	insert(200, 100, "openai", `[{"kind":"failover:upstream_429"}]`)
	// account 101 (newapi): one recovered request, fewer hops -> sorts after 100.
	insert(200, 101, "newapi", `[{"kind":"failover"}]`)

	// EXCLUDED rows:
	//   final-failure (status>=400) — not recovered.
	insert(502, 100, "openai", `[{"kind":"failover"},{"kind":"failover"}]`)
	//   recovered but no upstream attempts (empty array).
	insert(200, 100, "openai", `[]`)
	//   recovered on a non-GPT-line platform — out of scope.
	insert(200, 200, "anthropic", `[{"kind":"failover"},{"kind":"failover"}]`)

	resp, err := repo.GetFailoverHopStats(ctx, &service.OpsFailoverHopStatsFilter{
		TimeRange: "1d",
		StartTime: windowStart,
		EndTime:   windowEnd,
		TopN:      10,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Items, 2, "accounts 100 (openai) + 101 (newapi) are in scope; anthropic excluded")
	require.EqualValues(t, 2, resp.Total)

	item := resp.Items[0]
	require.EqualValues(t, 100, item.AccountID, "highest total_failover_hops sorts first")
	require.Equal(t, "openai", item.Platform)
	require.EqualValues(t, 2, item.RecoveredCount, "2 recovered-200 requests")
	require.EqualValues(t, 3, item.TotalFailoverHops, "2 + 1 account-switch events (retry excluded)")
	require.EqualValues(t, 4, item.TotalWastedAttempts, "3 + 1 array elements (retry counted as wasted attempt)")
	require.NotNil(t, item.AvgFailoverHopsPerRecovered)
	require.InDelta(t, 1.5, *item.AvgFailoverHopsPerRecovered, 1e-6, "3 failover hops / 2 recovered requests")

	// TopN trims the rows but Total reports the true account count.
	trimmed, err := repo.GetFailoverHopStats(ctx, &service.OpsFailoverHopStatsFilter{
		TimeRange: "1d",
		StartTime: windowStart,
		EndTime:   windowEnd,
		TopN:      1,
	})
	require.NoError(t, err)
	require.Len(t, trimmed.Items, 1, "topN=1 returns one row")
	require.EqualValues(t, 100, trimmed.Items[0].AccountID)
	require.EqualValues(t, 2, trimmed.Total, "Total is the true account count, not the trimmed page size")
}
