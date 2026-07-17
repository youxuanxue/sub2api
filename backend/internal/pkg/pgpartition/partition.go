// Package pgpartition provides a small, table-agnostic monthly RANGE-partition
// retention mechanism: keep N future months provisioned and DROP whole partitions
// once their data is fully past the retention cutoff. It is the reusable core behind
// the data-layer partition program (WAVE 1: ops_system_logs; later: ops_error_logs,
// usage_logs) — replacing bloat-generating chunked DELETE retention with instant
// DROP PARTITION.
//
// It is a leaf package (stdlib + lib/pq only) so both the service and repository
// layers can use it without an import cycle. All identifiers are passed as trusted
// constants by callers and quoted with pq.QuoteIdentifier; this package never takes
// SQL from untrusted input.
package pgpartition

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

// pgPartitionOverlapCode is the SQLSTATE Postgres raises when a CREATE ... PARTITION
// OF would overlap an existing partition (e.g. the legacy historical partition still
// covers the current month right after conversion). It is benign for EnsureMonthly:
// the month is already covered, so we skip it.
const pgPartitionOverlapCode = "42P17"

// DB is the minimal executor pgpartition needs; *sql.DB satisfies it.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// IsPartitioned reports whether `table` is a partitioned (parent) table.
func IsPartitioned(ctx context.Context, db DB, table string) (bool, error) {
	const q = `
		SELECT EXISTS(
			SELECT 1 FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = $1
		)`
	var partitioned bool
	if err := db.QueryRowContext(ctx, q, table).Scan(&partitioned); err != nil {
		return false, fmt.Errorf("pgpartition: is-partitioned %s: %w", table, err)
	}
	return partitioned, nil
}

// EnsureMonthly creates monthly RANGE partitions `<table>_YYYYMM` for the current
// month through `monthsAhead` months in the future, so live inserts always have a
// home as the calendar rolls forward. Months already covered by an existing partition
// (e.g. a legacy mega-partition right after conversion) raise 42P17 and are skipped.
// It never creates PAST months — those are either covered already or were intentionally
// dropped by retention, and recreating them would resurrect an empty partition.
// Idempotent (CREATE ... IF NOT EXISTS + overlap-skip).
func EnsureMonthly(ctx context.Context, db DB, table string, now time.Time, monthsAhead int) error {
	base := monthStartUTC(now)
	for m := 0; m <= monthsAhead; m++ {
		start := base.AddDate(0, m, 0)
		end := start.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%s", table, start.Format("200601"))
		q := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
			pq.QuoteIdentifier(name),
			pq.QuoteIdentifier(table),
			pq.QuoteLiteral(start.Format("2006-01-02")),
			pq.QuoteLiteral(end.Format("2006-01-02")),
		)
		if _, err := db.ExecContext(ctx, q); err != nil {
			if isOverlap(err) {
				continue // month already covered (e.g. legacy partition) — benign
			}
			return fmt.Errorf("pgpartition: ensure %s: %w", name, err)
		}
	}
	return nil
}

// DropExpired drops every child partition of `table` whose newest `timeCol` value is
// strictly before `cutoff` (or which is empty) — i.e. fully past the retention window.
// It judges by DATA (max(timeCol)), not partition-name parsing, so it correctly handles
// both monthly partitions and the wide legacy mega-partition created at conversion.
// Returns the estimated number of rows reclaimed (sum of dropped partitions' reltuples,
// for heartbeat/observability). Never drops the parent.
func DropExpired(ctx context.Context, db DB, table, timeCol string, cutoff time.Time) (int64, error) {
	const listQ = `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`
	rows, err := db.QueryContext(ctx, listQ, table)
	if err != nil {
		return 0, fmt.Errorf("pgpartition: list partitions of %s: %w", table, err)
	}
	var children []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("pgpartition: scan partition name: %w", scanErr)
		}
		children = append(children, name)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("pgpartition: iterate partitions: %w", rowsErr)
	}
	_ = rows.Close()

	var reclaimed int64
	for _, name := range children {
		var maxT sql.NullTime
		maxQ := fmt.Sprintf("SELECT max(%s) FROM %s", pq.QuoteIdentifier(timeCol), pq.QuoteIdentifier(name))
		if err := db.QueryRowContext(ctx, maxQ).Scan(&maxT); err != nil {
			return reclaimed, fmt.Errorf("pgpartition: max(%s) on %s: %w", timeCol, name, err)
		}
		if maxT.Valid && !maxT.Time.Before(cutoff) {
			continue // still has data within the retention window — keep
		}
		var est sql.NullInt64
		_ = db.QueryRowContext(ctx, "SELECT reltuples::bigint FROM pg_class WHERE relname = $1", name).Scan(&est)
		if _, err := db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", pq.QuoteIdentifier(name))); err != nil {
			return reclaimed, fmt.Errorf("pgpartition: drop %s: %w", name, err)
		}
		if est.Valid && est.Int64 > 0 {
			reclaimed += est.Int64
		}
	}
	return reclaimed, nil
}

// ListStraddling returns child partitions of `table` that DropExpired cannot drop
// (their newest timeCol value is NOT before cutoff — they still hold in-window
// rows) yet which ALSO contain rows older than cutoff (oldest timeCol value is
// before cutoff). This is the wide "legacy" mega-partition created at conversion:
// it absorbs current writes (so it can never be dropped) while accumulating
// expired rows. Without row-level reclaim such a partition grows unbounded — the
// prod disk-fill root cause where retention "runs" but reclaims 0. Callers feed
// the returned partitions to a capped chunked DELETE.
func ListStraddling(ctx context.Context, db DB, table, timeCol string, cutoff time.Time) ([]string, error) {
	const listQ = `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = $1`
	rows, err := db.QueryContext(ctx, listQ, table)
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list partitions of %s: %w", table, err)
	}
	var children []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("pgpartition: scan partition name: %w", scanErr)
		}
		children = append(children, name)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("pgpartition: iterate partitions: %w", rowsErr)
	}
	_ = rows.Close()

	var straddling []string
	for _, name := range children {
		var minT, maxT sql.NullTime
		q := fmt.Sprintf(
			"SELECT min(%s), max(%s) FROM %s",
			pq.QuoteIdentifier(timeCol), pq.QuoteIdentifier(timeCol), pq.QuoteIdentifier(name),
		)
		if err := db.QueryRowContext(ctx, q).Scan(&minT, &maxT); err != nil {
			return nil, fmt.Errorf("pgpartition: min/max(%s) on %s: %w", timeCol, name, err)
		}
		// Empty, or fully past the window (max < cutoff) → DropExpired handles it.
		if !maxT.Valid || maxT.Time.Before(cutoff) {
			continue
		}
		// Holds in-window rows (can't be dropped) AND has expired rows → straddling.
		if minT.Valid && minT.Time.Before(cutoff) {
			straddling = append(straddling, name)
		}
	}
	return straddling, nil
}

func isOverlap(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code) == pgPartitionOverlapCode
	}
	return false
}

func monthStartUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}
