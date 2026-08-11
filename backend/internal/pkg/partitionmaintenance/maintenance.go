// Package partitionmaintenance owns the partition targets, provisioning windows,
// and post-create coverage checks shared by scheduled and one-shot maintenance.
package partitionmaintenance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
)

const (
	JobName = "ops_partition_maintenance"

	opsMonthsAhead = 3
	usageDaysAhead = 7

	qaRecordsTable       = "qa_records"
	qaRecordsTimeColumn  = "created_at"
	defaultRehomeBatchSz = 5000
)

type Mode uint8

const (
	ModeAllowUnpartitioned Mode = iota
	ModeRequireAllPartitioned
)

type TableResult struct {
	Table         string                           `json:"table"`
	RangeCount    int                              `json:"range_count"`
	DefaultRehome *pgpartition.RehomeDefaultResult `json:"default_rehome,omitempty"`
}

type Result struct {
	Tables []TableResult `json:"tables"`
}

func (r Result) String() string {
	parts := make([]string, 0, len(r.Tables))
	for _, table := range r.Tables {
		parts = append(parts, fmt.Sprintf("%s:%d", table.Table, table.RangeCount))
	}
	return strings.Join(parts, ",")
}

type cadence uint8

const (
	cadenceMonthly cadence = iota
	cadenceDaily
)

type target struct {
	table   string
	cadence cadence
	ahead   int
}

var targets = [...]target{
	{table: "ops_system_logs", cadence: cadenceMonthly, ahead: opsMonthsAhead},
	{table: "ops_error_logs", cadence: cadenceMonthly, ahead: opsMonthsAhead},
	{table: "qa_records", cadence: cadenceMonthly, ahead: opsMonthsAhead},
	{table: "usage_logs", cadence: cadenceDaily, ahead: usageDaysAhead},
}

type partitionRange struct {
	start time.Time
	end   time.Time
}

func Ensure(
	ctx context.Context,
	db pgpartition.DB,
	now time.Time,
	mode Mode,
) (Result, error) {
	result := Result{Tables: make([]TableResult, 0, len(targets))}
	if db == nil {
		return result, fmt.Errorf("partitionmaintenance: database is required")
	}
	if mode != ModeAllowUnpartitioned && mode != ModeRequireAllPartitioned {
		return result, fmt.Errorf("partitionmaintenance: unsupported mode %d", mode)
	}

	for _, target := range targets {
		partitioned, err := pgpartition.IsPartitioned(ctx, db, target.table)
		if err != nil {
			return result, fmt.Errorf("partitionmaintenance: inspect %s: %w", target.table, err)
		}
		if !partitioned {
			if mode == ModeRequireAllPartitioned {
				return result, fmt.Errorf("partitionmaintenance: %s is not partitioned", target.table)
			}
			continue
		}

		ranges := targetRanges(target, now)
		var defaultRehome *pgpartition.RehomeDefaultResult
		if target.table == qaRecordsTable {
			rehome, rehomeErr := pgpartition.RehomeDefaultMonthly(
				ctx,
				db,
				qaRecordsTable,
				qaRecordsTimeColumn,
				now,
				defaultRehomeBatchSz,
			)
			if rehomeErr != nil {
				return result, fmt.Errorf("partitionmaintenance: rehome %s default: %w", target.table, rehomeErr)
			}
			if rehome.DefaultPartition != "" || len(rehome.Months) > 0 || rehome.RemainingRows > 0 {
				defaultRehome = &rehome
			}
		}

		switch target.cadence {
		case cadenceMonthly:
			err = pgpartition.EnsureMonthly(ctx, db, target.table, now, target.ahead)
		case cadenceDaily:
			err = pgpartition.EnsureDaily(ctx, db, target.table, now, target.ahead)
		default:
			err = fmt.Errorf("unsupported cadence %d", target.cadence)
		}
		if err != nil {
			return result, fmt.Errorf("partitionmaintenance: ensure %s: %w", target.table, err)
		}

		covered, err := countCoveredRanges(ctx, db, target.table, ranges)
		if err != nil {
			return result, err
		}
		if covered != len(ranges) {
			return result, fmt.Errorf(
				"partitionmaintenance: %s covers %d of %d required ranges",
				target.table,
				covered,
				len(ranges),
			)
		}
		tableResult := TableResult{Table: target.table, RangeCount: covered}
		if defaultRehome != nil {
			tableResult.DefaultRehome = defaultRehome
		}
		result.Tables = append(result.Tables, tableResult)
	}

	return result, nil
}

func targetRanges(target target, now time.Time) []partitionRange {
	u := now.UTC()
	var base time.Time
	if target.cadence == cadenceMonthly {
		base = time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	} else {
		base = time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
	}

	ranges := make([]partitionRange, 0, target.ahead+1)
	for offset := 0; offset <= target.ahead; offset++ {
		start := base.AddDate(0, 0, offset)
		end := start.AddDate(0, 0, 1)
		if target.cadence == cadenceMonthly {
			start = base.AddDate(0, offset, 0)
			end = start.AddDate(0, 1, 0)
		}
		ranges = append(ranges, partitionRange{start: start, end: end})
	}
	return ranges
}

func countCoveredRanges(
	ctx context.Context,
	db pgpartition.DB,
	table string,
	ranges []partitionRange,
) (int, error) {
	starts := make([]time.Time, 0, len(ranges))
	ends := make([]time.Time, 0, len(ranges))
	for _, item := range ranges {
		starts = append(starts, item.start)
		ends = append(ends, item.end)
	}

	// A converted table keeps its history in an attached legacy partition declared
	// FROM (MINVALUE), which is how tk_035 / tk_037 / the usage_logs cutover leave
	// prod: the current month or day is served by that partition alone, and the
	// matching CREATE is skipped as a benign overlap. Treating MINVALUE as an
	// unparseable bound would drop that partition from the union and report a real,
	// writable range as uncovered. Bounds we cannot classify (DEFAULT, LIST) are
	// still excluded so an unrecognized topology fails closed instead of counting
	// as covered.
	const query = `
WITH child_bounds AS (
  SELECT pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr
  FROM pg_inherits inheritance
  JOIN pg_class child ON child.oid = inheritance.inhrelid
  WHERE inheritance.inhparent = to_regclass($1)
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
		return 0, fmt.Errorf("partitionmaintenance: verify %s coverage: %w", table, err)
	}
	return covered, nil
}
