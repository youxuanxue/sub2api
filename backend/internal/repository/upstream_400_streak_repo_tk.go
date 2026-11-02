package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// upstream400StreakRepository reads open upstream-400 streaks for the TK
// advisory sentinel. Read-only; see service/upstream_400_streak_sentinel_tk.go.
type upstream400StreakRepository struct {
	db *sql.DB
}

// NewUpstream400StreakRepository builds the read-only streak reader.
func NewUpstream400StreakRepository(db *sql.DB) service.Upstream400StreakReader {
	return &upstream400StreakRepository{db: db}
}

// upstream400StreakSQL finds, per active account, the streak of identical
// upstream 400s that is still open: after the account's last successful
// request (usage_logs) and after the last upstream 400 with a different
// signature. Signature = lower-cased upstream message with digits / hex ids
// collapsed, so request ids and account numbers do not split one failure.
//
// $1 lookback window start, $2 minimum streak length, $3 minimum span seconds.
const upstream400StreakSQL = `
WITH errs AS (
  SELECT o.account_id, o.created_at, o.user_id,
         COALESCE(NULLIF(o.upstream_error_message, ''), o.error_message, '') AS msg
  FROM ops_error_logs o
  JOIN accounts a ON a.id = o.account_id AND a.deleted_at IS NULL AND a.status = 'active'
  WHERE o.created_at >= $1 AND o.upstream_status_code = 400 AND o.account_id IS NOT NULL
),
ok AS (
  SELECT x.account_id,
         (SELECT max(u.created_at) FROM usage_logs u
           WHERE u.account_id = x.account_id AND u.created_at >= $1) AS last_ok
  FROM (SELECT DISTINCT account_id FROM errs) x
),
e AS (
  SELECT errs.*, left(regexp_replace(lower(errs.msg), '[0-9a-f]{6,}|[0-9]+', '#', 'g'), 160) AS sig
  FROM errs JOIN ok USING (account_id)
  WHERE ok.last_ok IS NULL OR errs.created_at > ok.last_ok
),
latest AS (
  SELECT DISTINCT ON (account_id) account_id, sig, msg
  FROM e ORDER BY account_id, created_at DESC
),
brk AS (
  SELECT e.account_id, max(e.created_at) AS t
  FROM e JOIN latest l USING (account_id)
  WHERE e.sig <> l.sig GROUP BY e.account_id
)
SELECT e.account_id, l.sig, l.msg, count(*), min(e.created_at), max(e.created_at),
       count(DISTINCT e.user_id)
FROM e
JOIN latest l ON l.account_id = e.account_id AND l.sig = e.sig
LEFT JOIN brk b ON b.account_id = e.account_id
WHERE b.t IS NULL OR e.created_at > b.t
GROUP BY e.account_id, l.sig, l.msg
HAVING count(*) >= $2 AND extract(epoch FROM max(e.created_at) - min(e.created_at)) >= $3
ORDER BY count(*) DESC`

func (r *upstream400StreakRepository) ListOpenUpstream400Streaks(ctx context.Context, since time.Time, minCount int, minSpan time.Duration) ([]service.Upstream400Streak, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil upstream 400 streak repository")
	}
	rows, err := r.db.QueryContext(ctx, upstream400StreakSQL, since, minCount, minSpan.Seconds())
	if err != nil {
		return nil, fmt.Errorf("query upstream 400 streaks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []service.Upstream400Streak
	for rows.Next() {
		var s service.Upstream400Streak
		if err := rows.Scan(&s.AccountID, &s.Signature, &s.SampleMessage, &s.Count, &s.FirstAt, &s.LastAt, &s.DistinctUsers); err != nil {
			return nil, fmt.Errorf("scan upstream 400 streak: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
