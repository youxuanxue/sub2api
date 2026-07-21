package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

const (
	opsCleanupDefaultSchedule  = "0 2 * * *"
	opsCleanupBatchSize        = 5000
	opsCleanupCronStopTimeout  = 3 * time.Second
	opsCleanupRunTimeout       = 30 * time.Minute
	opsCleanupHeartbeatTimeout = 2 * time.Second
	// opsPartitionMonthsAhead is how many future monthly partitions to keep
	// provisioned for partitioned ops tables (e.g. ops_system_logs after tk_035),
	// so live inserts always have a home as the calendar rolls forward. The daily
	// cleanup tick re-ensures these; the conversion migration seeds the first two.
	opsPartitionMonthsAhead = 3
	// opsStraddleReclaimMaxRowsPerRun caps how many expired rows the daily cleanup
	// will chunk-DELETE from a straddling partition (the wide legacy mega-partition
	// that can't be dropped because it also absorbs current writes) in a single run.
	// Capping keeps the first reclaim of a large backlog online-safe; the remainder
	// drains over subsequent daily runs, and steady-state daily volume is far below
	// the cap.
	opsStraddleReclaimMaxRowsPerRun = 1_000_000
)

type opsCleanupTarget struct {
	retentionDays int
	table         string
	timeCol       string
	castDate      bool
	counter       *int64
}

type opsCleanupDeletedCounts struct {
	errorLogs      int64
	ingressRejects int64
	alertEvents    int64
	systemLogs     int64
	logAudits      int64
	systemMetrics  int64
	hourlyPreagg   int64
	dailyPreagg    int64
}

func (c opsCleanupDeletedCounts) String() string {
	return fmt.Sprintf(
		"error_logs=%d ingress_rejects=%d alert_events=%d system_logs=%d log_audits=%d system_metrics=%d hourly_preagg=%d daily_preagg=%d",
		c.errorLogs,
		c.ingressRejects,
		c.alertEvents,
		c.systemLogs,
		c.logAudits,
		c.systemMetrics,
		c.hourlyPreagg,
		c.dailyPreagg,
	)
}

// opsCleanupPlan 把"保留天数"翻译成具体的清理动作。
//   - days < 0  → 跳过该项清理（ok=false），保留兼容老数据
//   - days == 0 → TRUNCATE TABLE（O(1) 全清），truncate=true
//   - days > 0  → 批量 DELETE 早于 now-N天 的行，cutoff = now - N 天
func opsCleanupPlan(now time.Time, days int) (cutoff time.Time, truncate, ok bool) {
	if days < 0 {
		return time.Time{}, false, false
	}
	if days == 0 {
		return time.Time{}, true, true
	}
	return now.AddDate(0, 0, -days), false, true
}

func opsCleanupRunOne(
	ctx context.Context,
	db *sql.DB,
	truncate bool,
	cutoff time.Time,
	table, timeCol string,
	castDate bool,
	batchSize int,
) (int64, error) {
	if truncate {
		return truncateOpsTable(ctx, db, table)
	}
	// Partitioned ops tables (e.g. ops_system_logs once tk_035 has converted it):
	// retention is instant DROP PARTITION + keeping future months provisioned, not a
	// bloat-generating chunked DELETE. The branch is keyed on the live partition state
	// (not a hardcoded table list), so ops_error_logs / usage_logs auto-adopt it when
	// their own conversion lands. Pre-conversion (plain table) falls through to DELETE.
	partitioned, err := pgpartition.IsPartitioned(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if partitioned {
		if err := pgpartition.EnsureMonthly(ctx, db, table, time.Now().UTC(), opsPartitionMonthsAhead); err != nil {
			return 0, err
		}
		dropped, err := pgpartition.DropExpired(ctx, db, table, timeCol, cutoff)
		if err != nil {
			return dropped, err
		}
		// Whole-partition DROP cannot reclaim a partition that still holds in-window
		// rows — notably the wide legacy mega-partition that also absorbs current
		// writes (its max(timeCol) is always "now"). Left alone it grows unbounded,
		// which is the prod disk-fill root cause where retention runs daily yet
		// reclaims 0. Fall back to a capped chunked DELETE of its expired rows so
		// retention actually bounds it at the configured window.
		straddling, err := pgpartition.ListStraddling(ctx, db, table, timeCol, cutoff)
		if err != nil {
			return dropped, err
		}
		reclaimed := dropped
		remaining := opsStraddleReclaimMaxRowsPerRun
		for _, child := range straddling {
			if remaining <= 0 {
				break
			}
			n, delErr := deleteOldRowsByID(ctx, db, child, timeCol, cutoff, batchSize, false, remaining)
			reclaimed += n
			remaining -= int(n)
			if delErr != nil {
				return reclaimed, delErr
			}
		}
		return reclaimed, nil
	}
	return deleteOldRowsByID(ctx, db, table, timeCol, cutoff, batchSize, castDate, 0)
}

func deleteOldRowsByID(
	ctx context.Context,
	db *sql.DB,
	table string,
	timeColumn string,
	cutoff time.Time,
	batchSize int,
	castCutoffToDate bool,
	maxRows int,
) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = opsCleanupBatchSize
	}

	where := fmt.Sprintf("%s < $1", timeColumn)
	if castCutoffToDate {
		where = fmt.Sprintf("%s < $1::date", timeColumn)
	}

	q := fmt.Sprintf(`
WITH batch AS (
  SELECT id FROM %s
  WHERE %s
  ORDER BY id
  LIMIT $2
)
DELETE FROM %s
WHERE id IN (SELECT id FROM batch)
`, table, where, table)

	var total int64
	for {
		res, err := db.ExecContext(ctx, q, cutoff, batchSize)
		if err != nil {
			if isMissingRelationError(err) {
				return total, nil
			}
			return total, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected == 0 {
			break
		}
		// Per-run cap (maxRows <= 0 means unlimited) — keeps a large backlog reclaim
		// online-safe; the remainder drains over subsequent runs.
		if maxRows > 0 && total >= int64(maxRows) {
			break
		}
	}
	return total, nil
}

// truncateOpsTable 用 TRUNCATE TABLE 清空指定表，先 SELECT COUNT(*) 取得清空前行数用于 heartbeat。
func truncateOpsTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var count int64
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	if count == 0 {
		return 0, nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("truncate %s: %w", table, err)
	}
	return count, nil
}

func isMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist") && strings.Contains(s, "relation")
}
