package repository

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CountRoutingCapacityRejections counts ops_error_logs rows whose
// error_phase = 'routing' over the filter's window/scope — the client-visible
// "no available accounts" empty-pool fast-fail (429, #575) plus relayed
// cc-<edge> mirror downstream-capacity rejections.
//
// Why a dedicated query (not a field on OpsDashboardOverview): error_phase is
// the persisted discriminator that is set EXCLUSIVELY for capacity rejections
// (ops_error_logger.go classifyOpsPhase + the routingCapacityLimited override),
// so a single COUNT FILTER isolates them from user-level rate limits (phase
// upstream/request/auth) with no new column. These rows are
// is_business_limited=true and are therefore excluded from the dashboard's
// error_rate/upstream_error_rate (numerator AND denominator) — so without this
// count a thin-pool-race empty-pool storm is invisible to every ratio alert
// rule, and (cooling no account) to the account-/pool-level incident channels
// too. Keeping it off GetDashboardOverview avoids both an extra aggregate on the
// dashboard hot path and a half-populated overview field across the raw vs
// pre-aggregated query paths. The alert evaluator calls this for the
// routing_capacity_rejection_count metric; see tk_035 for the seeded P0 rule.
//
// Scope/time come from buildErrorWhere (the same predicate the dashboard error
// counts use): created_at window, is_count_tokens=FALSE, optional platform and
// group_id. No status/is_business_limited filter is needed — error_phase
// 'routing' already implies a capacity rejection.
func (r *opsRepository) CountRoutingCapacityRejections(ctx context.Context, filter *service.OpsDashboardFilter) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return 0, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return 0, fmt.Errorf("start_time/end_time required")
	}

	where, args, _ := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	q := `SELECT COALESCE(COUNT(*) FILTER (WHERE error_phase = 'routing'), 0) AS routing_capacity_rejections
FROM ops_error_logs
` + where

	var n int64
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// TopRoutingCapacityRejectionCauses returns the top-`limit` platforms by
// routing-phase rejection count over the filter window/scope, descending. It
// lets a fired routing_capacity_rejection_count P0 card name WHICH platform
// pool(s) ran out of capacity — the platform comes from the API key's group
// (apiKey.Group.Platform), so it is populated even though no account was
// selected (see ops_error_logger.go). Best-effort: the caller drops the cause
// line on any error rather than blocking the alert.
func (r *opsRepository) TopRoutingCapacityRejectionCauses(ctx context.Context, filter *service.OpsDashboardFilter, limit int) ([]*service.OpsRoutingRejectionCause, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	if limit <= 0 {
		limit = 2
	}

	where, args, next := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	q := `SELECT COALESCE(NULLIF(TRIM(platform), ''), '(unknown)') AS platform, COUNT(*) AS cnt
FROM ops_error_logs
` + where + `
  AND error_phase = 'routing'
GROUP BY 1
ORDER BY cnt DESC, platform ASC
LIMIT $` + strconv.Itoa(next)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsRoutingRejectionCause, 0, limit)
	for rows.Next() {
		var c service.OpsRoutingRejectionCause
		if err := rows.Scan(&c.Platform, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
