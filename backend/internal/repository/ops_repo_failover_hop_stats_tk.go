package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetFailoverHopStats computes the per-account "wasted failover hops" KPI for the
// OpenAI/GPT line (PR #899 follow-up observability). A recovered-200 row in
// ops_error_logs (client status < 400 but upstream attempts occurred) carries one
// upstream_errors element per FAILED upstream attempt — the successful final
// attempt is never appended — so jsonb_array_length(upstream_errors) is exactly the
// wasted-hop count for that request. We scope to recovered rows on openai/newapi and
// group by (account, platform). Read-time aggregate (no rollup/migration), reusing
// buildErrorWhere so the window/platform/group scope is byte-identical to the rest of
// the dashboard — mirrors GetTopErrorCause.
func (r *opsRepository) GetFailoverHopStats(ctx context.Context, filter *service.OpsFailoverHopStatsFilter) (*service.OpsFailoverHopStatsResponse, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	if filter.StartTime.After(filter.EndTime) {
		return nil, fmt.Errorf("start_time must be <= end_time")
	}

	dashboardFilter := &service.OpsDashboardFilter{
		StartTime: filter.StartTime.UTC(),
		EndTime:   filter.EndTime.UTC(),
		Platform:  strings.TrimSpace(strings.ToLower(filter.Platform)),
		GroupID:   filter.GroupID,
	}

	where, args, idx := buildErrorWhere(dashboardFilter, dashboardFilter.StartTime, dashboardFilter.EndTime, 1)
	// Recovered-200 rows on the GPT line that actually hopped. When a platform
	// filter is already applied by buildErrorWhere, the IN clause is redundant but
	// harmless; without one it keeps scope to openai/newapi.
	where += ` AND account_id IS NOT NULL
  AND COALESCE(status_code, 0) < 400
  AND upstream_errors IS NOT NULL
  AND upstream_errors <> 'null'::jsonb
  AND jsonb_array_length(upstream_errors) > 0
  AND platform IN ('openai', 'newapi')`

	// failover_hops counts only account-switch events (same predicate as the
	// fleet-wide queryAccountSwitchCount); wasted_attempts counts every failed
	// upstream attempt regardless of kind.
	recoveredCTE := `
WITH recovered AS (
  SELECT
    account_id,
    platform,
    (
      SELECT COUNT(*)
      FROM jsonb_array_elements(upstream_errors) AS ev
      WHERE split_part(ev->>'kind', ':', 1) IN ('failover', 'retry_exhausted_failover', 'failover_on_400')
    ) AS failover_hops,
    jsonb_array_length(upstream_errors) AS wasted_attempts
  FROM ops_error_logs ` + where + `
)`

	// True total = distinct (account, platform) groups with recovered hops, so the
	// TopN footer reports the real account count rather than the trimmed page size.
	var total int64
	if err := r.db.QueryRowContext(ctx,
		recoveredCTE+`
SELECT COUNT(*) FROM (SELECT 1 FROM recovered GROUP BY account_id, platform) g`,
		args...,
	).Scan(&total); err != nil {
		return nil, err
	}

	q := recoveredCTE + `
SELECT
  recovered.account_id,
  COALESCE(NULLIF(a.name, ''), '(account ' || recovered.account_id::text || ')') AS account_name,
  recovered.platform,
  COUNT(*)::bigint                                  AS recovered_count,
  COALESCE(SUM(recovered.failover_hops), 0)::bigint AS total_failover_hops,
  COALESCE(SUM(recovered.wasted_attempts), 0)::bigint AS total_wasted_attempts,
  ROUND(AVG(recovered.failover_hops)::numeric, 3)::float8 AS avg_failover_hops_per_recovered
FROM recovered
LEFT JOIN accounts a ON a.id = recovered.account_id
GROUP BY recovered.account_id, a.name, recovered.platform
ORDER BY total_failover_hops DESC, recovered_count DESC, recovered.account_id ASC
LIMIT $` + fmt.Sprintf("%d", idx)
	queryArgs := append(append([]any{}, args...), filter.TopN)

	rows, err := r.db.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]*service.OpsFailoverHopStatsItem, 0, filter.TopN)
	for rows.Next() {
		item := &service.OpsFailoverHopStatsItem{}
		var avgHops sql.NullFloat64
		if err := rows.Scan(
			&item.AccountID,
			&item.AccountName,
			&item.Platform,
			&item.RecoveredCount,
			&item.TotalFailoverHops,
			&item.TotalWastedAttempts,
			&avgHops,
		); err != nil {
			return nil, err
		}
		if avgHops.Valid {
			v := avgHops.Float64
			item.AvgFailoverHopsPerRecovered = &v
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &service.OpsFailoverHopStatsResponse{
		TimeRange: strings.TrimSpace(filter.TimeRange),
		StartTime: dashboardFilter.StartTime,
		EndTime:   dashboardFilter.EndTime,
		Platform:  dashboardFilter.Platform,
		GroupID:   dashboardFilter.GroupID,
		Items:     items,
		Total:     total,
		TopN:      filter.TopN,
	}, nil
}
