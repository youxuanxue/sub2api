package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// CountUserVisibleFailures counts terminal failures that a real, attributable
// customer request saw. It deliberately ignores recovered upstream rows
// (status_code < 400) and count_tokens probes via buildErrorWhere.
func (r *opsRepository) CountUserVisibleFailures(ctx context.Context, filter *service.OpsDashboardFilter, ownerScope string) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return 0, fmt.Errorf("nil filter")
	}
	where, args, _ := buildUserVisibleFailureWhere(filter, ownerScope, 1)
	q := `SELECT COALESCE(COUNT(*), 0) FROM ops_error_logs ` + where

	var n int64
	if err := r.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func (r *opsRepository) GetUserVisibleFailureBreakdown(ctx context.Context, filter *service.OpsDashboardFilter, ownerScope string, limit int) (*service.OpsUserVisibleFailureBreakdown, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	if filter == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 3
	}

	failures, err := r.CountUserVisibleFailures(ctx, filter, ownerScope)
	if err != nil {
		return nil, err
	}
	successes, _, err := r.queryUsageCounts(ctx, filter, filter.StartTime.UTC(), filter.EndTime.UTC())
	if err != nil {
		return nil, err
	}

	users, err := r.topUserVisibleFailureUsers(ctx, filter, ownerScope, limit)
	if err != nil {
		return nil, err
	}
	surfaces, err := r.topUserVisibleFailureSurfaces(ctx, filter, ownerScope, limit)
	if err != nil {
		return nil, err
	}
	roots, err := r.topUserVisibleFailureRoots(ctx, filter, ownerScope, limit)
	if err != nil {
		return nil, err
	}

	return &service.OpsUserVisibleFailureBreakdown{
		Failures:  failures,
		Successes: successes,
		Users:     users,
		Surfaces:  surfaces,
		Roots:     roots,
	}, nil
}

func buildUserVisibleFailureWhere(filter *service.OpsDashboardFilter, ownerScope string, startIndex int) (string, []any, int) {
	where, args, next := buildErrorWhere(filter, filter.StartTime.UTC(), filter.EndTime.UTC(), startIndex)
	where += " AND COALESCE(status_code, 0) >= 400"
	where += " AND COALESCE(user_id, deleted_key_owner_user_id) IS NOT NULL"
	switch strings.TrimSpace(ownerScope) {
	case "client":
		where += " AND COALESCE(error_owner, '') = 'client'"
	default:
		where += " AND COALESCE(error_owner, '') IN ('provider', 'platform')"
	}
	return where, args, next
}

func (r *opsRepository) topUserVisibleFailureUsers(ctx context.Context, filter *service.OpsDashboardFilter, ownerScope string, limit int) ([]*service.OpsUserVisibleFailureUser, error) {
	where, args, next := buildUserVisibleFailureWhere(filter, ownerScope, 1)
	args = append(args, limit)
	q := `WITH base AS (
  SELECT * FROM ops_error_logs ` + where + `
)
SELECT
  COALESCE(l.user_id, l.deleted_key_owner_user_id, 0) AS user_id,
  COALESCE(u.email, l.deleted_key_owner_email, '') AS user_email,
  COALESCE(ak.name, l.deleted_key_name, '') AS api_key_name,
  COALESCE(g.name, '') AS group_name,
  COUNT(*) AS n
FROM base l
LEFT JOIN users u ON u.id = COALESCE(l.user_id, l.deleted_key_owner_user_id)
LEFT JOIN api_keys ak ON ak.id = l.api_key_id AND ak.deleted_at IS NULL
LEFT JOIN groups g ON g.id = l.group_id AND g.deleted_at IS NULL
GROUP BY 1, 2, 3, 4
ORDER BY n DESC, user_id ASC
LIMIT $` + fmt.Sprintf("%d", next)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsUserVisibleFailureUser, 0, limit)
	for rows.Next() {
		row := &service.OpsUserVisibleFailureUser{}
		if err := rows.Scan(&row.UserID, &row.UserEmail, &row.APIKeyName, &row.GroupName, &row.Count); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *opsRepository) topUserVisibleFailureSurfaces(ctx context.Context, filter *service.OpsDashboardFilter, ownerScope string, limit int) ([]*service.OpsUserVisibleFailureSurface, error) {
	where, args, next := buildUserVisibleFailureWhere(filter, ownerScope, 1)
	args = append(args, limit)
	q := `WITH base AS (
  SELECT * FROM ops_error_logs ` + where + `
)
SELECT
  COALESCE(status_code, 0) AS status_code,
  COALESCE(upstream_status_code, 0) AS upstream_status_code,
  COALESCE(error_type, '') AS error_type,
  COUNT(*) AS n
FROM base
GROUP BY 1, 2, 3
ORDER BY n DESC, status_code ASC, upstream_status_code ASC
LIMIT $` + fmt.Sprintf("%d", next)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsUserVisibleFailureSurface, 0, limit)
	for rows.Next() {
		row := &service.OpsUserVisibleFailureSurface{}
		if err := rows.Scan(&row.StatusCode, &row.UpstreamStatusCode, &row.ErrorType, &row.Count); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *opsRepository) topUserVisibleFailureRoots(ctx context.Context, filter *service.OpsDashboardFilter, ownerScope string, limit int) ([]*service.OpsUserVisibleFailureRoot, error) {
	where, args, next := buildUserVisibleFailureWhere(filter, ownerScope, 1)
	args = append(args, limit)
	q := `WITH base AS (
  SELECT * FROM ops_error_logs ` + where + `
)
SELECT
  COALESCE(error_phase, '') AS phase,
  COALESCE(error_owner, '') AS owner,
  COALESCE(platform, '') AS platform,
  COALESCE(NULLIF(requested_model, ''), NULLIF(model, ''), '(unknown)') AS model,
  COALESCE(account_id, 0) AS account_id,
  left(regexp_replace(COALESCE(upstream_error_message, error_message, ''), E'[\\n\\r]+', ' ', 'g'), 96) AS msg,
  COUNT(*) AS n
FROM base
GROUP BY 1, 2, 3, 4, 5, 6
ORDER BY n DESC, platform ASC, model ASC
LIMIT $` + fmt.Sprintf("%d", next)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*service.OpsUserVisibleFailureRoot, 0, limit)
	for rows.Next() {
		row := &service.OpsUserVisibleFailureRoot{}
		var accountID sql.NullInt64
		if err := rows.Scan(&row.Phase, &row.Owner, &row.Platform, &row.Model, &accountID, &row.Message, &row.Count); err != nil {
			return nil, err
		}
		if accountID.Valid {
			row.AccountID = accountID.Int64
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
