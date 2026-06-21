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

// TopRoutingCapacityRejectionByPlatform returns, for the top-`platformLimit`
// platforms by routing-phase rejection count over the filter window/scope, each
// platform's total rejection count and its top-`usersPerPlatform` contributing
// users (user id + api-key name + count, descending). It powers the
// routing_capacity_rejection_count P0 card's single joint "主因" line, which names
// WHICH pool(s) ran out of capacity AND WHO inside each pool is driving the
// rejections — the platform→user attribution the two old marginal queries
// (platform-only + user-only) could not express, since a user spanning two
// platforms smeared across both margins.
//
// platform, user_id and api_key_id are all populated from the authenticated key
// on every routing rejection (ops_error_logger.go, same block), so attribution is
// reliable even though no account was selected. Two queries assembled in Go:
//   - platform totals: COUNT(*) over ALL routing rows per platform — faithful to
//     the metric, so rows with no attributable user_id are still counted;
//   - per-platform users: COUNT(*) per (platform, user_id, api_key_id) over routing
//     rows with user_id IS NOT NULL, window-ranked within each platform, with the
//     operator-assigned api-key NAME resolved by a LEFT JOIN to api_keys (or the
//     deleted-key snapshot for a hard-deleted key). The key SECRET is never read.
//
// Best-effort: the caller drops the 主因 line on any error rather than blocking the
// alert.
func (r *opsRepository) TopRoutingCapacityRejectionByPlatform(ctx context.Context, filter *service.OpsDashboardFilter, platformLimit, usersPerPlatform int) ([]*service.OpsRoutingRejectionPlatform, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, fmt.Errorf("nil filter")
	}
	if filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("start_time/end_time required")
	}
	if platformLimit <= 0 {
		platformLimit = 2
	}
	if usersPerPlatform <= 0 {
		usersPerPlatform = 3
	}

	// Query A — platform totals (top-platformLimit, INCLUDES rows with NULL
	// user_id so the per-platform count stays faithful to the overall metric).
	whereA, argsA, nextA := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	qA := `SELECT COALESCE(NULLIF(TRIM(platform), ''), '(unknown)') AS platform, COUNT(*) AS cnt
FROM ops_error_logs
` + whereA + `
  AND error_phase = 'routing'
GROUP BY 1
ORDER BY cnt DESC, platform ASC
LIMIT $` + strconv.Itoa(nextA)
	argsA = append(argsA, platformLimit)

	rowsA, err := r.db.QueryContext(ctx, qA, argsA...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rowsA.Close() }()

	out := make([]*service.OpsRoutingRejectionPlatform, 0, platformLimit)
	idx := make(map[string]int, platformLimit)
	for rowsA.Next() {
		var p service.OpsRoutingRejectionPlatform
		if err := rowsA.Scan(&p.Platform, &p.Count); err != nil {
			return nil, err
		}
		idx[p.Platform] = len(out)
		out = append(out, &p)
	}
	if err := rowsA.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	// Query B — top contributing users per platform (user_id IS NOT NULL),
	// window-ranked within each platform. Bucketed by (platform, user_id, api_key_id)
	// so a user's distinct keys surface separately (each with its own name); the
	// api-key NAME is resolved by a LEFT JOIN to api_keys, with the deleted-key
	// snapshot as fallback. Platforms outside Query A's top set are dropped in Go,
	// so no platform-id array param is needed.
	whereB, argsB, nextB := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), 1)
	qB := `SELECT platform, user_id, key_name, cnt
FROM (
  SELECT platform, user_id, key_name, cnt,
         ROW_NUMBER() OVER (PARTITION BY platform ORDER BY cnt DESC, user_id ASC, key_name ASC) AS rn
  FROM (
    SELECT g.platform,
           g.user_id,
           COALESCE(NULLIF(TRIM(ak.name), ''), NULLIF(TRIM(g.deleted_key_name), ''), '') AS key_name,
           g.cnt
    FROM (
      SELECT COALESCE(NULLIF(TRIM(platform), ''), '(unknown)') AS platform,
             user_id,
             api_key_id,
             MAX(deleted_key_name) AS deleted_key_name,
             COUNT(*) AS cnt
      FROM ops_error_logs
      ` + whereB + `
        AND error_phase = 'routing'
        AND user_id IS NOT NULL
      GROUP BY 1, user_id, api_key_id
    ) g
    LEFT JOIN api_keys ak ON ak.id = g.api_key_id
  ) named
) ranked
WHERE rn <= $` + strconv.Itoa(nextB) + `
ORDER BY platform ASC, cnt DESC, user_id ASC, key_name ASC`
	argsB = append(argsB, usersPerPlatform)

	rowsB, err := r.db.QueryContext(ctx, qB, argsB...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rowsB.Close() }()

	for rowsB.Next() {
		var (
			platform string
			u        service.OpsRoutingRejectionUser
		)
		if err := rowsB.Scan(&platform, &u.UserID, &u.APIKeyName, &u.Count); err != nil {
			return nil, err
		}
		if i, ok := idx[platform]; ok {
			user := u
			out[i].Users = append(out[i].Users, &user)
		}
	}
	if err := rowsB.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
