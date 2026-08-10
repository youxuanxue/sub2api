package pgpartition

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

const defaultRehomeBatchSize = 5000

// RehomeMonthResult reports one month moved out of the DEFAULT partition.
type RehomeMonthResult struct {
	Partition string `json:"partition"`
	Start     string `json:"start"`
	End       string `json:"end"`
	RowsMoved int64  `json:"rows_moved"`
}

// RehomeDefaultResult summarizes DEFAULT rehome work for observability receipts.
type RehomeDefaultResult struct {
	DefaultPartition string              `json:"default_partition"`
	RemainingRows    int64               `json:"remaining_default_rows"`
	Months           []RehomeMonthResult `json:"months,omitempty"`
}

// RehomeDefaultMonthly moves rows from a table's DEFAULT child partition into
// bounded monthly partitions. Future months are expected to already exist from
// EnsureMonthly; this routine creates and ATTACHes any missing month partitions
// before batch-moving matching rows out of DEFAULT.
func RehomeDefaultMonthly(
	ctx context.Context,
	db DB,
	table, timeCol string,
	now time.Time,
	batchSize int,
) (RehomeDefaultResult, error) {
	result := RehomeDefaultResult{}
	if db == nil {
		return result, fmt.Errorf("pgpartition: database is required")
	}
	table = strings.TrimSpace(table)
	timeCol = strings.TrimSpace(timeCol)
	if table == "" || timeCol == "" {
		return result, fmt.Errorf("pgpartition: table and time column are required")
	}
	if batchSize <= 0 {
		batchSize = defaultRehomeBatchSize
	}

	defaultName, ok, err := defaultChildPartition(ctx, db, table)
	if err != nil {
		return result, err
	}
	if !ok {
		result.RemainingRows = 0
		return result, nil
	}
	result.DefaultPartition = defaultName

	months, err := distinctMonthsInDefault(ctx, db, defaultName, timeCol)
	if err != nil {
		return result, err
	}
	for _, monthStart := range orderRehomeMonths(months, now) {
		monthEnd := monthStart.AddDate(0, 1, 0)
		partitionName := fmt.Sprintf("%s_%s", table, monthStart.Format("200601"))
		moved, err := rehomeDefaultMonth(ctx, db, table, defaultName, partitionName, timeCol, monthStart, monthEnd, batchSize)
		if err != nil {
			return result, fmt.Errorf("pgpartition: rehome %s: %w", partitionName, err)
		}
		if moved > 0 {
			result.Months = append(result.Months, RehomeMonthResult{
				Partition: partitionName,
				Start:     monthStart.Format(time.RFC3339),
				End:       monthEnd.Format(time.RFC3339),
				RowsMoved: moved,
			})
		}
	}

	remaining, err := countDefaultRows(ctx, db, defaultName)
	if err != nil {
		return result, err
	}
	result.RemainingRows = remaining
	return result, nil
}

func defaultChildPartition(ctx context.Context, db DB, table string) (string, bool, error) {
	const q = `
		SELECT c.relname,
		       pg_get_expr(c.relpartbound, c.oid, true) AS bound_expr
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE i.inhparent = to_regclass($1)`
	rows, err := db.QueryContext(ctx, q, table)
	if err != nil {
		return "", false, fmt.Errorf("pgpartition: list default child of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, boundExpr string
		if scanErr := rows.Scan(&name, &boundExpr); scanErr != nil {
			return "", false, fmt.Errorf("pgpartition: scan default child: %w", scanErr)
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(boundExpr)), "DEFAULT") {
			return name, true, nil
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return "", false, fmt.Errorf("pgpartition: iterate default child: %w", rowsErr)
	}
	return "", false, nil
}

func distinctMonthsInDefault(ctx context.Context, db DB, defaultName, timeCol string) ([]time.Time, error) {
	q := fmt.Sprintf(
		`SELECT DISTINCT date_trunc('month', %s AT TIME ZONE 'UTC')::timestamptz
		   FROM %s
		  WHERE %s IS NOT NULL
		  ORDER BY 1`,
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(defaultName),
		pq.QuoteIdentifier(timeCol),
	)
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list default months: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var months []time.Time
	for rows.Next() {
		var month time.Time
		if scanErr := rows.Scan(&month); scanErr != nil {
			return nil, fmt.Errorf("pgpartition: scan default month: %w", scanErr)
		}
		months = append(months, monthStartUTC(month))
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("pgpartition: iterate default months: %w", rowsErr)
	}
	return months, nil
}

func rehomeDefaultMonth(
	ctx context.Context,
	db DB,
	table, defaultName, partitionName, timeCol string,
	start, end time.Time,
	batchSize int,
) (int64, error) {
	attached, err := childPartitionExists(ctx, db, table, partitionName)
	if err != nil {
		return 0, err
	}
	if !attached {
		createQ := fmt.Sprintf(
			"CREATE TABLE %s (LIKE %s INCLUDING ALL)",
			pq.QuoteIdentifier(partitionName),
			pq.QuoteIdentifier(table),
		)
		if _, err := db.ExecContext(ctx, createQ); err != nil {
			return 0, fmt.Errorf("create staging partition %s: %w", partitionName, err)
		}
	}

	var moved int64
	for {
		insertQ := fmt.Sprintf(
			`WITH moved AS (
			    DELETE FROM %s
			     WHERE ctid IN (
			           SELECT ctid
			             FROM %s
			            WHERE %s >= $1 AND %s < $2
			            LIMIT $3
			     )
			     RETURNING *
			)
			INSERT INTO %s SELECT * FROM moved`,
			pq.QuoteIdentifier(defaultName),
			pq.QuoteIdentifier(defaultName),
			pq.QuoteIdentifier(timeCol),
			pq.QuoteIdentifier(timeCol),
			pq.QuoteIdentifier(partitionName),
		)
		res, err := db.ExecContext(ctx, insertQ, start, end, batchSize)
		if err != nil {
			return moved, fmt.Errorf("batch move into %s: %w", partitionName, err)
		}
		batch, err := res.RowsAffected()
		if err != nil {
			return moved, fmt.Errorf("inspect batch move into %s: %w", partitionName, err)
		}
		if batch == 0 {
			break
		}
		moved += batch
	}

	if moved == 0 && !attached {
		dropQ := "DROP TABLE IF EXISTS " + pq.QuoteIdentifier(partitionName)
		if _, err := db.ExecContext(ctx, dropQ); err != nil {
			return moved, fmt.Errorf("drop unused staging partition %s: %w", partitionName, err)
		}
		return 0, nil
	}

	if !attached {
		attachQ := fmt.Sprintf(
			"ALTER TABLE %s ATTACH PARTITION %s FOR VALUES FROM (%s) TO (%s)",
			pq.QuoteIdentifier(table),
			pq.QuoteIdentifier(partitionName),
			pq.QuoteLiteral(start.Format(time.RFC3339)),
			pq.QuoteLiteral(end.Format(time.RFC3339)),
		)
		if _, err := db.ExecContext(ctx, attachQ); err != nil {
			return moved, fmt.Errorf("attach partition %s: %w", partitionName, err)
		}
	}
	return moved, nil
}

func childPartitionExists(ctx context.Context, db DB, table, partitionName string) (bool, error) {
	const q = `
		SELECT EXISTS (
		  SELECT 1
		    FROM pg_inherits i
		    JOIN pg_class child ON child.oid = i.inhrelid
		    JOIN pg_class parent ON parent.oid = i.inhparent
		   WHERE parent.relname = $1
		     AND child.relname = $2
		)`
	var exists bool
	if err := db.QueryRowContext(ctx, q, table, partitionName).Scan(&exists); err != nil {
		return false, fmt.Errorf("pgpartition: inspect child %s of %s: %w", partitionName, table, err)
	}
	return exists, nil
}

func countDefaultRows(ctx context.Context, db DB, defaultName string) (int64, error) {
	q := "SELECT COUNT(*) FROM " + pq.QuoteIdentifier(defaultName)
	var count int64
	if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
		return 0, fmt.Errorf("pgpartition: count %s rows: %w", defaultName, err)
	}
	return count, nil
}

func orderRehomeMonths(months []time.Time, now time.Time) []time.Time {
	current := monthStartUTC(now)
	var currentMonth, future, past []time.Time
	for _, month := range months {
		month = monthStartUTC(month)
		switch {
		case month.Equal(current):
			currentMonth = append(currentMonth, month)
		case month.After(current):
			future = append(future, month)
		default:
			past = append(past, month)
		}
	}
	sort.Slice(future, func(i, j int) bool { return future[i].Before(future[j]) })
	sort.Slice(past, func(i, j int) bool { return past[i].Before(past[j]) })
	ordered := make([]time.Time, 0, len(months))
	ordered = append(ordered, currentMonth...)
	ordered = append(ordered, future...)
	ordered = append(ordered, past...)
	return ordered
}

// RemainingDefaultRows exposes DEFAULT child row count for health probes.
func RemainingDefaultRows(ctx context.Context, db DB, table string) (int64, error) {
	defaultName, ok, err := defaultChildPartition(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	return countDefaultRows(ctx, db, defaultName)
}
