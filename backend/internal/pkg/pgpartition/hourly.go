package pgpartition

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
)

const (
	// QARecordsHourlyHorizon is the future UTC-hour partition window provisioned for qa_records.
	QARecordsHourlyHorizon = 72
	qaRecordsTableName     = "qa_records"
)

// HourStartUTC truncates t to the UTC hour boundary.
func HourStartUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), u.Hour(), 0, 0, 0, time.UTC)
}

// HourlyPartitionName returns qa_records_YYYYMMDD_HH for the given UTC hour start.
func HourlyPartitionName(table string, hourStart time.Time) string {
	h := hourStart.UTC()
	return fmt.Sprintf("%s_%s_%02d", table, h.Format("20060102"), h.Hour())
}

// RetentionBoundary returns the catalog-derived hot retention cutoff:
// date_trunc('hour', now) - 24 hours.
func RetentionBoundary(now time.Time) time.Time {
	return HourStartUTC(now).Add(-24 * time.Hour)
}

// EnsureHourly creates UTC-hour RANGE partitions from the current hour through
// hoursAhead inclusive. Each child covers [hour, hour+1h). Idempotent with overlap skip.
func EnsureHourly(ctx context.Context, db DB, table string, now time.Time, hoursAhead int) error {
	if hoursAhead < 0 {
		return fmt.Errorf("pgpartition: hoursAhead must be non-negative")
	}
	base := HourStartUTC(now)
	for h := 0; h <= hoursAhead; h++ {
		start := base.Add(time.Duration(h) * time.Hour)
		end := start.Add(time.Hour)
		name := HourlyPartitionName(table, start)
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
			return fmt.Errorf("pgpartition: ensure hourly %s: %w", name, err)
		}
	}
	return nil
}

// ChildPartitionBound describes one direct child partition bound from pg_catalog.
type ChildPartitionBound struct {
	Schema         string
	Name           string
	BoundExpr      string
	Lower          time.Time
	Upper          time.Time
	LowerUnbounded bool
	UpperUnbounded bool
	IsDefault      bool
}

// ListChildPartitionBounds returns direct child bounds for a partitioned table.
func ListChildPartitionBounds(ctx context.Context, db DB, table string) ([]ChildPartitionBound, error) {
	const listQ = `
		SELECT
			n.nspname,
			c.relname,
			pg_get_expr(c.relpartbound, c.oid, true) AS bound_expr,
			pg_get_expr(c.relpartbound, c.oid, true) LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
			pg_get_expr(c.relpartbound, c.oid, true) LIKE '%TO (MAXVALUE)' AS upper_unbounded,
			pg_get_expr(c.relpartbound, c.oid, true) = 'DEFAULT' AS is_default,
			substring(
				pg_get_expr(c.relpartbound, c.oid, true)
				FROM $$FROM \('([^']+)'\)$$
			)::timestamptz AS lower_bound,
			substring(
				pg_get_expr(c.relpartbound, c.oid, true)
				FROM $$TO \('([^']+)'\)$$
			)::timestamptz AS upper_bound
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.inhparent = to_regclass($1)
		ORDER BY n.nspname, c.relname`
	rows, err := db.QueryContext(ctx, listQ, table)
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list child bounds of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	var out []ChildPartitionBound
	for rows.Next() {
		var child ChildPartitionBound
		var lower, upper sql.NullTime
		if scanErr := rows.Scan(
			&child.Schema, &child.Name, &child.BoundExpr,
			&child.LowerUnbounded, &child.UpperUnbounded, &child.IsDefault,
			&lower, &upper,
		); scanErr != nil {
			return nil, fmt.Errorf("pgpartition: scan child bound: %w", scanErr)
		}
		if child.IsDefault {
			out = append(out, child)
			continue
		}
		if child.LowerUnbounded || child.UpperUnbounded || !lower.Valid || !upper.Valid {
			return nil, fmt.Errorf(
				"pgpartition: partition %s.%s has non-hour canonical bound: %s",
				child.Schema, child.Name, child.BoundExpr,
			)
		}
		child.Lower = lower.Time
		child.Upper = upper.Time
		if child.Upper.Sub(child.Lower) != time.Hour {
			return nil, fmt.Errorf(
				"pgpartition: partition %s.%s is not exactly one hour: %s",
				child.Schema, child.Name, child.BoundExpr,
			)
		}
		out = append(out, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgpartition: iterate child bounds: %w", err)
	}
	return out, nil
}

// CountCoveredHourlyRanges reports how many required [start,end) hour ranges are covered.
func CountCoveredHourlyRanges(ctx context.Context, db DB, table string, ranges []HourlyRange) (int, error) {
	starts := make([]time.Time, 0, len(ranges))
	ends := make([]time.Time, 0, len(ranges))
	for _, item := range ranges {
		starts = append(starts, item.Start)
		ends = append(ends, item.End)
	}
	const query = `
WITH child_bounds AS (
  SELECT pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr
  FROM pg_inherits inheritance
  JOIN pg_class child ON child.oid = inheritance.inhrelid
  WHERE inheritance.inhparent = to_regclass($1)
    AND pg_get_expr(child.relpartbound, child.oid, true) <> 'DEFAULT'
), parsed_bounds AS (
  SELECT
    bound_expr LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
    bound_expr LIKE '%TO (MAXVALUE)' AS upper_unbounded,
    substring(bound_expr FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
    substring(bound_expr FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
  FROM child_bounds
), covered_union AS (
  SELECT range_agg(tstzrange(
    CASE WHEN lower_unbounded THEN NULL ELSE lower_bound END,
    CASE WHEN upper_unbounded THEN NULL ELSE upper_bound END,
    '[)'
  )) AS ranges
  FROM parsed_bounds
  WHERE (lower_unbounded OR lower_bound IS NOT NULL)
    AND (upper_unbounded OR upper_bound IS NOT NULL)
), required_ranges AS (
  SELECT lower_bound, upper_bound
  FROM unnest($2::timestamptz[], $3::timestamptz[]) AS required(lower_bound, upper_bound)
)
SELECT count(*)
FROM required_ranges, covered_union
WHERE covered_union.ranges @> tstzrange(required_ranges.lower_bound, required_ranges.upper_bound, '[)')`
	var covered int
	if err := db.QueryRowContext(ctx, query, table, pq.Array(starts), pq.Array(ends)).Scan(&covered); err != nil {
		return 0, fmt.Errorf("pgpartition: verify hourly coverage: %w", err)
	}
	return covered, nil
}

// HourlyRange is a half-open UTC-hour partition window.
type HourlyRange struct {
	Start time.Time
	End   time.Time
}

// HourlyTargetRanges returns required UTC-hour ranges from now through hoursAhead.
func HourlyTargetRanges(now time.Time, hoursAhead int) []HourlyRange {
	base := HourStartUTC(now)
	ranges := make([]HourlyRange, 0, hoursAhead+1)
	for offset := 0; offset <= hoursAhead; offset++ {
		start := base.Add(time.Duration(offset) * time.Hour)
		ranges = append(ranges, HourlyRange{Start: start, End: start.Add(time.Hour)})
	}
	return ranges
}

// DefaultChildPartition returns the DEFAULT child name if present.
func DefaultChildPartition(ctx context.Context, db DB, table string) (string, bool, error) {
	children, err := ListChildPartitionBounds(ctx, db, table)
	if err != nil {
		return "", false, err
	}
	for _, child := range children {
		if child.IsDefault {
			return child.Name, true, nil
		}
	}
	return "", false, nil
}

// CountTableRows counts rows in a single relation.
func CountTableRows(ctx context.Context, db DB, table string) (int64, error) {
	return countTableRows(ctx, db, table)
}

func countTableRows(ctx context.Context, db DB, table string) (int64, error) {
	var count int64
	q := fmt.Sprintf("SELECT COUNT(*) FROM %s", pq.QuoteIdentifier(table))
	if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
		return 0, fmt.Errorf("pgpartition: count %s: %w", table, err)
	}
	return count, nil
}
