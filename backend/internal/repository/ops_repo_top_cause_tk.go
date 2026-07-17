package repository

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// TK (us7 P0 2026-06-13): top-offender breakdown behind a fired error-rate
// alert. Reuses buildErrorWhere so the window + platform/group scope are
// byte-identical to the metric the alert evaluated (queryErrorCounts), then
// narrows to the rows that actually drive the breached metric and groups by
// (model, owner, upstream_status). See service.computeTopCause for the caller.
func (r *opsRepository) GetTopErrorCause(ctx context.Context, filter *service.OpsDashboardFilter, upstreamOnly bool, limit int) ([]*service.OpsTopErrorCause, error) {
	if filter == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 2
	}
	where, args, idx := buildErrorWhere(filter, filter.StartTime, filter.EndTime, 1)

	// Narrow to the rows behind the breached metric:
	//   upstream_error_rate -> upstream_excl set (provider-owned, non-429/529 final failures);
	//   error_rate          -> all SLA errors (non-business-limited final failures).
	if upstreamOnly {
		where += " AND COALESCE(status_code, 0) >= 400 AND error_owner = 'provider' AND COALESCE(upstream_status_code, status_code, 0) NOT IN (429, 529)"
	} else {
		where += " AND COALESCE(status_code, 0) >= 400"
	}

	q := `
SELECT
  COALESCE(NULLIF(requested_model, ''), NULLIF(model, ''), '(unknown)') AS model,
  COALESCE(NULLIF(error_owner, ''), 'unknown')                          AS owner,
  COALESCE(upstream_status_code, status_code, 0)                        AS upstream_status,
  COUNT(*)                                                              AS n
FROM ops_error_logs ` + where + `
GROUP BY 1, 2, 3
ORDER BY n DESC, model ASC
LIMIT $` + fmt.Sprintf("%d", idx)
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsTopErrorCause, 0, limit)
	for rows.Next() {
		c := &service.OpsTopErrorCause{}
		if err := rows.Scan(&c.Model, &c.ErrorOwner, &c.UpstreamStatus, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
