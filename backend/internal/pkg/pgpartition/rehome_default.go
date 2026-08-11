package pgpartition

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	defaultRehomeBatchSize    = 5000
	defaultRehomeMaxRowsPerRun = 20000
)

// RehomeOptions bounds one rehome invocation. BatchSize caps each SQL page;
// MaxRowsPerRun caps total rows moved across all months (0 = unlimited).
type RehomeOptions struct {
	BatchSize     int
	MaxRowsPerRun int64
}

func (o RehomeOptions) withDefaults() RehomeOptions {
	if o.BatchSize <= 0 {
		o.BatchSize = defaultRehomeBatchSize
	}
	if o.MaxRowsPerRun <= 0 {
		o.MaxRowsPerRun = defaultRehomeMaxRowsPerRun
	}
	return o
}

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
	RowsMoved        int64               `json:"rows_moved"`
	BudgetExhausted  bool                `json:"budget_exhausted,omitempty"`
	Months           []RehomeMonthResult `json:"months,omitempty"`
}

// RehomeDefaultMonthly moves rows from a table's DEFAULT child partition into
// bounded monthly partitions. Rows are drained into a detached staging table
// before CREATE TABLE ... PARTITION OF so PostgreSQL never rejects the attach
// with "updated partition constraint would be violated". EnsureMonthly should
// run after rehome so future empty months are provisioned without blocking moves.
func RehomeDefaultMonthly(
	ctx context.Context,
	db DB,
	table, timeCol string,
	now time.Time,
	opts RehomeOptions,
) (RehomeDefaultResult, error) {
	opts = opts.withDefaults()
	result := RehomeDefaultResult{}
	if db == nil {
		return result, fmt.Errorf("pgpartition: database is required")
	}
	table = strings.TrimSpace(table)
	timeCol = strings.TrimSpace(timeCol)
	if table == "" || timeCol == "" {
		return result, fmt.Errorf("pgpartition: table and time column are required")
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

	var rowsBudget int64 = opts.MaxRowsPerRun
	for _, monthStart := range orderRehomeMonths(months, now) {
		if rowsBudget <= 0 {
			result.BudgetExhausted = true
			break
		}
		monthEnd := monthStart.AddDate(0, 1, 0)
		partitionName, err := resolveMonthlyPartitionName(ctx, db, table, monthStart)
		if err != nil {
			return result, err
		}
		moved, exhausted, err := rehomeDefaultMonth(
			ctx, db, table, defaultName, partitionName, timeCol,
			monthStart, monthEnd, opts.BatchSize, rowsBudget,
		)
		if err != nil {
			return result, fmt.Errorf("pgpartition: rehome %s: %w", partitionName, err)
		}
		result.RowsMoved += moved
		rowsBudget -= moved
		if exhausted {
			result.BudgetExhausted = true
			break
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

func rehomeStagingName(partitionName string) string {
	return partitionName + "_rehome_staging"
}

func rehomeDefaultMonth(
	ctx context.Context,
	db DB,
	table, defaultName, partitionName, timeCol string,
	start, end time.Time,
	batchSize int,
	rowBudget int64,
) (moved int64, budgetExhausted bool, err error) {
	attached, err := childPartitionExists(ctx, db, table, partitionName)
	if err != nil {
		return 0, false, err
	}
	if attached {
		return moveRowsBetweenTables(
			ctx, db, defaultName, partitionName, timeCol, start, end, batchSize, rowBudget,
		)
	}

	stagingName := rehomeStagingName(partitionName)
	stagingExists, err := relationExists(ctx, db, stagingName)
	if err != nil {
		return 0, false, err
	}
	remainingInDefault, err := countRowsInRange(ctx, db, defaultName, timeCol, start, end)
	if err != nil {
		return 0, false, err
	}
	if stagingExists && remainingInDefault == 0 {
		finalized, finalizeErr := finalizeStagingPartition(ctx, db, table, partitionName, stagingName, start, end)
		if finalizeErr != nil {
			return 0, false, finalizeErr
		}
		if finalized {
			return 0, false, nil
		}
	}

	if err := ensureRehomeStaging(ctx, db, defaultName, stagingName); err != nil {
		return 0, false, err
	}

	moved, budgetExhausted, err = moveRowsBetweenTables(
		ctx, db, defaultName, stagingName, timeCol, start, end, batchSize, rowBudget,
	)
	if err != nil {
		return moved, budgetExhausted, err
	}
	if budgetExhausted {
		return moved, true, nil
	}

	remainingInDefault, err = countRowsInRange(ctx, db, defaultName, timeCol, start, end)
	if err != nil {
		return moved, false, err
	}
	if remainingInDefault > 0 {
		return moved, false, nil
	}

	if _, err := finalizeStagingPartition(ctx, db, table, partitionName, stagingName, start, end); err != nil {
		return moved, false, err
	}
	return moved, false, nil
}

func ensureRehomeStaging(ctx context.Context, db DB, defaultName, stagingName string) error {
	q := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (LIKE %s INCLUDING DEFAULTS INCLUDING GENERATED)",
		pq.QuoteIdentifier(stagingName),
		pq.QuoteIdentifier(defaultName),
	)
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("ensure rehome staging %s: %w", stagingName, err)
	}
	return nil
}

func finalizeStagingPartition(
	ctx context.Context,
	db DB,
	table, partitionName, stagingName string,
	start, end time.Time,
) (bool, error) {
	stagingRows, err := countTableRows(ctx, db, stagingName)
	if err != nil {
		return false, err
	}
	if stagingRows == 0 {
		dropQ := "DROP TABLE IF EXISTS " + pq.QuoteIdentifier(stagingName)
		if _, err := db.ExecContext(ctx, dropQ); err != nil {
			return false, fmt.Errorf("drop empty rehome staging %s: %w", stagingName, err)
		}
		return false, nil
	}

	attached, err := childPartitionExists(ctx, db, table, partitionName)
	if err != nil {
		return false, err
	}
	if !attached {
		createQ := fmt.Sprintf(
			"CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
			pq.QuoteIdentifier(partitionName),
			pq.QuoteIdentifier(table),
			pq.QuoteLiteral(start.Format(time.RFC3339)),
			pq.QuoteLiteral(end.Format(time.RFC3339)),
		)
		if _, err := db.ExecContext(ctx, createQ); err != nil {
			if !isOverlap(err) {
				return false, fmt.Errorf("create attached partition %s: %w", partitionName, err)
			}
		}
	}

	insertQ := fmt.Sprintf(
		"INSERT INTO %s SELECT * FROM %s",
		pq.QuoteIdentifier(partitionName),
		pq.QuoteIdentifier(stagingName),
	)
	if err := execInTx(ctx, db, func(tx DB) error {
		if _, err := tx.ExecContext(ctx, insertQ); err != nil {
			return fmt.Errorf("copy staging into %s: %w", partitionName, err)
		}
		dropQ := "DROP TABLE IF EXISTS " + pq.QuoteIdentifier(stagingName)
		if _, err := tx.ExecContext(ctx, dropQ); err != nil {
			return fmt.Errorf("drop rehome staging %s: %w", stagingName, err)
		}
		return nil
	}); err != nil {
		return false, err
	}
	return true, nil
}

func moveRowsBetweenTables(
	ctx context.Context,
	db DB,
	sourceName, destName, timeCol string,
	start, end time.Time,
	batchSize int,
	rowBudget int64,
) (moved int64, budgetExhausted bool, err error) {
	if rowBudget <= 0 {
		return moved, true, nil
	}
	limit := batchSize
	if int64(limit) > rowBudget {
		limit = int(rowBudget)
	}
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
		pq.QuoteIdentifier(sourceName),
		pq.QuoteIdentifier(sourceName),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(destName),
	)
	res, err := db.ExecContext(ctx, insertQ, start, end, limit)
	if err != nil {
		return moved, false, fmt.Errorf("batch move %s -> %s: %w", sourceName, destName, err)
	}
	batch, err := res.RowsAffected()
	if err != nil {
		return moved, false, fmt.Errorf("inspect batch move %s -> %s: %w", sourceName, destName, err)
	}
	if batch == 0 {
		return moved, false, nil
	}
	moved += batch
	rowBudget -= batch
	if rowBudget <= 0 {
		return moved, true, nil
	}
	if batch < int64(limit) {
		return moved, false, nil
	}
	extra, exhausted, err := moveRowsBetweenTables(
		ctx, db, sourceName, destName, timeCol, start, end, batchSize, rowBudget,
	)
	return moved + extra, exhausted, err
}

type txBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func execInTx(ctx context.Context, db DB, fn func(DB) error) error {
	beginner, ok := db.(txBeginner)
	if !ok {
		return fn(db)
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgpartition: begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgpartition: commit tx: %w", err)
	}
	return nil
}

func relationExists(ctx context.Context, db DB, name string) (bool, error) {
	const q = `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`
	var exists bool
	if err := db.QueryRowContext(ctx, q, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("pgpartition: inspect relation %s: %w", name, err)
	}
	return exists, nil
}

func countTableRows(ctx context.Context, db DB, table string) (int64, error) {
	q := "SELECT COUNT(*) FROM " + pq.QuoteIdentifier(table)
	var count int64
	if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
		return 0, fmt.Errorf("pgpartition: count %s rows: %w", table, err)
	}
	return count, nil
}

func countRowsInRange(ctx context.Context, db DB, table, timeCol string, start, end time.Time) (int64, error) {
	q := fmt.Sprintf(
		`SELECT COUNT(*) FROM %s WHERE %s >= $1 AND %s < $2`,
		pq.QuoteIdentifier(table),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(timeCol),
	)
	var count int64
	if err := db.QueryRowContext(ctx, q, start, end).Scan(&count); err != nil {
		return 0, fmt.Errorf("pgpartition: count %s rows in range: %w", table, err)
	}
	return count, nil
}

func monthlyPartitionNameCandidates(table string, monthStart time.Time) []string {
	canonical := MonthlyPartitionName(table, monthStart)
	compact := fmt.Sprintf("%s_%s", table, monthStart.Format("200601"))
	legacy := fmt.Sprintf("%s_%s", table, monthStart.Format("2006_01"))
	seen := make(map[string]struct{}, 3)
	var candidates []string
	for _, candidate := range []string{canonical, legacy, compact} {
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates
}

func resolveMonthlyPartitionName(ctx context.Context, db DB, table string, monthStart time.Time) (string, error) {
	for _, candidate := range monthlyPartitionNameCandidates(table, monthStart) {
		attached, err := childPartitionExists(ctx, db, table, candidate)
		if err != nil {
			return "", err
		}
		if attached {
			return candidate, nil
		}
	}
	return monthlyPartitionNameCandidates(table, monthStart)[0], nil
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
	return countTableRows(ctx, db, defaultName)
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
