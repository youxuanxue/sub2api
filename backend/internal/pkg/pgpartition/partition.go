// Package pgpartition provides small, table-agnostic RANGE-partition
// retention mechanisms: keep future ranges provisioned and DROP whole partitions
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

// DropExecutor is the smaller database surface needed for bound-based retention.
// Both *sql.DB and repository transaction/executor wrappers satisfy it.
type DropExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
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

func rejectQAHourlyOwner(table string) error {
	if table == qaRecordsTableName {
		return fmt.Errorf("pgpartition: qa_records is owned by EnsureHourly")
	}
	return nil
}

// EnsureMonthly creates monthly RANGE partitions for the current month through
// `monthsAhead` months in the future, so live inserts always have a home as the
// calendar rolls forward. Months already covered by an existing partition (e.g. a
// legacy mega-partition right after conversion) raise 42P17 and are skipped.
// It never creates PAST months — those are either covered already or were intentionally
// dropped by retention, and recreating them would resurrect an empty partition.
// Idempotent (CREATE ... IF NOT EXISTS + overlap-skip).
func EnsureMonthly(ctx context.Context, db DB, table string, now time.Time, monthsAhead int) error {
	if err := rejectQAHourlyOwner(table); err != nil {
		return err
	}
	base := monthStartUTC(now)
	for m := 0; m <= monthsAhead; m++ {
		start := base.AddDate(0, m, 0)
		end := start.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%s", table, start.Format("200601"))
		q := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
			pq.QuoteIdentifier(name),
			pq.QuoteIdentifier(table),
			pq.QuoteLiteral(start.Format(time.RFC3339)),
			pq.QuoteLiteral(end.Format(time.RFC3339)),
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

// EnsureDaily creates UTC daily partitions for the current day through daysAhead.
// It has the same overlap semantics as EnsureMonthly so the attach-legacy partition
// may cover the cutover day while tomorrow and later are still provisioned.
func EnsureDaily(ctx context.Context, db DB, table string, now time.Time, daysAhead int) error {
	if err := rejectQAHourlyOwner(table); err != nil {
		return err
	}
	base := dayStartUTC(now)
	for d := 0; d <= daysAhead; d++ {
		start := base.AddDate(0, 0, d)
		end := start.AddDate(0, 0, 1)
		name := fmt.Sprintf("%s_%s", table, start.Format("20060102"))
		q := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
			pq.QuoteIdentifier(name),
			pq.QuoteIdentifier(table),
			pq.QuoteLiteral(start.Format(time.RFC3339)),
			pq.QuoteLiteral(end.Format(time.RFC3339)),
		)
		if _, err := db.ExecContext(ctx, q); err != nil {
			if isOverlap(err) {
				continue
			}
			return fmt.Errorf("pgpartition: ensure %s: %w", name, err)
		}
	}
	return nil
}

// DropExpired drops every child partition of `table` whose exclusive upper bound is at
// or before `cutoff`. Partition bounds, rather than current row contents or partition
// names, are the retention authority: empty future partitions remain provisioned, while
// empty expired partitions can still be reclaimed. A bound that PostgreSQL cannot expose
// as a finite timestamptz aborts the operation before any partition is dropped.
// Returns the estimated number of rows reclaimed (sum of dropped partitions' reltuples,
// for heartbeat/observability). Never drops the parent.
func DropExpired(ctx context.Context, db DropExecutor, table string, cutoff time.Time) (int64, error) {
	if err := rejectQAHourlyOwner(table); err != nil {
		return 0, err
	}
	const listQ = `
		SELECT
			n.nspname,
			c.relname,
			pg_get_expr(c.relpartbound, c.oid, true) AS bound_expr,
			substring(
				pg_get_expr(c.relpartbound, c.oid, true)
				FROM $$TO \('([^']+)'\)$$
			)::timestamptz AS upper_bound,
			c.reltuples::bigint AS estimated_rows
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.inhparent = to_regclass($1)
		ORDER BY n.nspname, c.relname`
	rows, err := db.QueryContext(ctx, listQ, table)
	if err != nil {
		return 0, fmt.Errorf("pgpartition: list partitions of %s: %w", table, err)
	}
	type childPartition struct {
		schema        string
		name          string
		boundExpr     string
		upper         time.Time
		estimatedRows int64
	}
	var expired []childPartition
	for rows.Next() {
		var child childPartition
		var upper sql.NullTime
		if scanErr := rows.Scan(&child.schema, &child.name, &child.boundExpr, &upper, &child.estimatedRows); scanErr != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("pgpartition: scan partition bound: %w", scanErr)
		}
		if !upper.Valid {
			_ = rows.Close()
			return 0, fmt.Errorf(
				"pgpartition: partition %s.%s has no finite timestamptz upper bound: %s",
				child.schema, child.name, child.boundExpr,
			)
		}
		child.upper = upper.Time
		if !child.upper.After(cutoff) {
			expired = append(expired, child)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("pgpartition: iterate partitions: %w", rowsErr)
	}
	_ = rows.Close()

	var reclaimed int64
	for _, child := range expired {
		qualifiedName := pq.QuoteIdentifier(child.schema) + "." + pq.QuoteIdentifier(child.name)
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+qualifiedName); err != nil {
			return reclaimed, fmt.Errorf("pgpartition: drop %s: %w", qualifiedName, err)
		}
		if child.estimatedRows > 0 {
			reclaimed += child.estimatedRows
		}
	}
	return reclaimed, nil
}

// ListStraddling returns remaining child partitions that contain rows older than the
// cutoff. Callers invoke it after DropExpired, so these are bound-straddling partitions
// (notably the wide legacy partition) that cannot yet be dropped as a whole and need a
// capped row-level reclaim. Finite lower bounds at or after the cutoff prove that a
// current/future daily child cannot contain expired rows, avoiding one min query per day.
func ListStraddling(ctx context.Context, db DB, table, timeCol string, cutoff time.Time) ([]string, error) {
	const listQ = `
		SELECT
			n.nspname,
			c.relname,
			pg_get_expr(c.relpartbound, c.oid, true) AS bound_expr,
			pg_get_expr(c.relpartbound, c.oid, true) LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
			substring(
				pg_get_expr(c.relpartbound, c.oid, true)
				FROM $$FROM \('([^']+)'\)$$
			)::timestamptz AS lower_bound
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.inhparent = to_regclass($1)
		ORDER BY n.nspname, c.relname`
	rows, err := db.QueryContext(ctx, listQ, table)
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list partitions of %s: %w", table, err)
	}
	type childPartition struct {
		schema         string
		name           string
		boundExpr      string
		lowerUnbounded bool
		lower          sql.NullTime
	}
	var candidates []childPartition
	for rows.Next() {
		var child childPartition
		if scanErr := rows.Scan(
			&child.schema,
			&child.name,
			&child.boundExpr,
			&child.lowerUnbounded,
			&child.lower,
		); scanErr != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("pgpartition: scan partition lower bound: %w", scanErr)
		}
		if !child.lowerUnbounded && !child.lower.Valid {
			_ = rows.Close()
			return nil, fmt.Errorf(
				"pgpartition: partition %s.%s has no finite timestamptz lower bound: %s",
				child.schema, child.name, child.boundExpr,
			)
		}
		if child.lowerUnbounded || child.lower.Time.Before(cutoff) {
			candidates = append(candidates, child)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("pgpartition: iterate partitions: %w", rowsErr)
	}
	_ = rows.Close()

	var straddling []string
	for _, child := range candidates {
		var minT sql.NullTime
		qualifiedName := pq.QuoteIdentifier(child.schema) + "." + pq.QuoteIdentifier(child.name)
		q := fmt.Sprintf("SELECT min(%s) FROM %s", pq.QuoteIdentifier(timeCol), qualifiedName)
		if err := db.QueryRowContext(ctx, q).Scan(&minT); err != nil {
			return nil, fmt.Errorf("pgpartition: min(%s) on %s: %w", timeCol, qualifiedName, err)
		}
		if minT.Valid && minT.Time.Before(cutoff) {
			straddling = append(straddling, child.name)
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

func dayStartUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
