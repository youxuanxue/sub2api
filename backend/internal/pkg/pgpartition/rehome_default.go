package pgpartition

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

const (
	defaultRehomeBatchSize     = 5000
	defaultRehomeMaxRowsPerRun = 20000
	qaRecordsRehomeLockKey     = int64(0x715f726563) // "q_rec" — qa_records rehome finalize
)

var rehomeStagingSuffix = regexp.MustCompile(`^(.+)_rehome_staging$`)

// RehomeOptions bounds one rehome invocation. BatchSize caps each SQL page;
// MaxRowsPerRun caps total rows copied into staging per run (0 = unlimited).
// Rows are never deleted from DEFAULT until finalize commits.
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
	StagingRows      int64               `json:"staging_rows,omitempty"`
	RowsMoved        int64               `json:"rows_moved"`
	BudgetExhausted  bool                `json:"budget_exhausted,omitempty"`
	PendingFinalize  bool                `json:"pending_finalize,omitempty"`
	Months           []RehomeMonthResult `json:"months,omitempty"`
}

// RehomeDefaultMonthly moves rows from DEFAULT into bounded monthly partitions.
// Copy into detached staging preserves parent visibility until a single finalize
// transaction deletes the month from DEFAULT, attaches the partition, and drops
// staging. EnsureMonthly for qa_records must be skipped while DEFAULT or staging
// still holds in-progress months.
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

	months, err := rehomeMonthCandidates(ctx, db, table, defaultName, timeCol, now)
	if err != nil {
		return result, err
	}

	rowsBudget := opts.MaxRowsPerRun
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
		moved, exhausted, pending, err := rehomeDefaultMonth(
			ctx, db, table, defaultName, partitionName, timeCol,
			monthStart, monthEnd, opts.BatchSize, rowsBudget,
		)
		if err != nil {
			return result, fmt.Errorf("pgpartition: rehome %s: %w", partitionName, err)
		}
		result.RowsMoved += moved
		rowsBudget -= moved
		if pending {
			result.PendingFinalize = true
		}
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
	stagingRows, err := countAllRehomeStagingRows(ctx, db, table)
	if err != nil {
		return result, err
	}
	result.StagingRows = stagingRows
	if stagingRows > 0 {
		result.PendingFinalize = true
	}
	return result, nil
}

func rehomeMonthCandidates(
	ctx context.Context,
	db DB,
	table, defaultName, timeCol string,
	now time.Time,
) ([]time.Time, error) {
	fromDefault, err := distinctMonthsInDefault(ctx, db, defaultName, timeCol)
	if err != nil {
		return nil, err
	}
	fromStaging, err := distinctMonthsInRehomeStaging(ctx, db, table, timeCol, now)
	if err != nil {
		return nil, err
	}
	seen := make(map[time.Time]struct{}, len(fromDefault)+len(fromStaging))
	var merged []time.Time
	for _, month := range append(fromDefault, fromStaging...) {
		month = monthStartUTC(month)
		if _, ok := seen[month]; ok {
			continue
		}
		seen[month] = struct{}{}
		merged = append(merged, month)
	}
	return merged, nil
}

func distinctMonthsInRehomeStaging(
	ctx context.Context,
	db DB,
	table, timeCol string,
	now time.Time,
) ([]time.Time, error) {
	stagingTables, err := listRehomeStagingTables(ctx, db, table)
	if err != nil {
		return nil, err
	}
	seen := make(map[time.Time]struct{})
	var months []time.Time
	for _, stagingName := range stagingTables {
		partitionName := strings.TrimSuffix(stagingName, "_rehome_staging")
		monthStart, ok := monthStartFromPartitionName(table, partitionName)
		if !ok {
			continue
		}
		count, err := countTableRows(ctx, db, stagingName)
		if err != nil || count == 0 {
			continue
		}
		if _, ok := seen[monthStart]; ok {
			continue
		}
		seen[monthStart] = struct{}{}
		months = append(months, monthStart)
	}
	_ = now
	return months, nil
}

func listRehomeStagingTables(ctx context.Context, db DB, table string) ([]string, error) {
	prefix := table + "_"
	const q = `
		SELECT relname
		  FROM pg_class
		 WHERE relkind = 'r'
		   AND relname LIKE $1
		   AND relname LIKE '%\_rehome\_staging' ESCAPE '\'`
	rows, err := db.QueryContext(ctx, q, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("pgpartition: list rehome staging tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr != nil {
			return nil, fmt.Errorf("pgpartition: scan rehome staging table: %w", scanErr)
		}
		if rehomeStagingSuffix.MatchString(name) {
			names = append(names, name)
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, fmt.Errorf("pgpartition: iterate rehome staging tables: %w", rowsErr)
	}
	sort.Strings(names)
	return names, nil
}

func countAllRehomeStagingRows(ctx context.Context, db DB, table string) (int64, error) {
	names, err := listRehomeStagingTables(ctx, db, table)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, name := range names {
		count, err := countTableRows(ctx, db, name)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func monthStartFromPartitionName(table, partitionName string) (time.Time, bool) {
	prefix := table + "_"
	if !strings.HasPrefix(partitionName, prefix) {
		return time.Time{}, false
	}
	suffix := strings.TrimPrefix(partitionName, prefix)
	layouts := []string{"2006_01", "200601"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, suffix); err == nil {
			return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC), true
		}
	}
	return time.Time{}, false
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
) (copied int64, budgetExhausted, pendingFinalize bool, err error) {
	attached, err := childPartitionExists(ctx, db, table, partitionName)
	if err != nil {
		return 0, false, false, err
	}
	if attached {
		moved, exhausted, moveErr := moveRowsBetweenTables(
			ctx, db, defaultName, partitionName, timeCol, start, end, batchSize, rowBudget,
		)
		return moved, exhausted, false, moveErr
	}

	stagingName := rehomeStagingName(partitionName)
	if err := ensureRehomeStaging(ctx, db, defaultName, stagingName); err != nil {
		return 0, false, false, err
	}

	stagingRows, err := countRowsInRange(ctx, db, stagingName, timeCol, start, end)
	if err != nil {
		return 0, false, false, err
	}
	if stagingRows > 0 {
		finalized, finalizeErr := finalizeStagingPartition(
			ctx, db, table, defaultName, partitionName, stagingName, timeCol, start, end,
		)
		if finalizeErr != nil {
			return 0, false, true, finalizeErr
		}
		if finalized {
			return stagingRows, false, false, nil
		}
	}

	defaultRows, err := countRowsInRange(ctx, db, defaultName, timeCol, start, end)
	if err != nil {
		return 0, false, false, err
	}
	if defaultRows == 0 && stagingRows == 0 {
		return 0, false, false, nil
	}

	copied, budgetExhausted, err = copyDefaultToStaging(
		ctx, db, defaultName, stagingName, timeCol, start, end, batchSize, rowBudget,
	)
	if err != nil {
		return copied, budgetExhausted, stagingRows > 0 || defaultRows > 0, err
	}
	if budgetExhausted {
		return copied, true, true, nil
	}

	stagingRows, err = countRowsInRange(ctx, db, stagingName, timeCol, start, end)
	if err != nil {
		return copied, false, true, err
	}
	defaultRows, err = countRowsInRange(ctx, db, defaultName, timeCol, start, end)
	if err != nil {
		return copied, false, true, err
	}
	if defaultRows == 0 && stagingRows > 0 {
		finalized, err := finalizeStagingPartition(
			ctx, db, table, defaultName, partitionName, stagingName, timeCol, start, end,
		)
		if err != nil {
			return copied, false, true, err
		}
		if finalized {
			return stagingRows, false, false, nil
		}
		return copied, false, true, nil
	}
	ready, err := defaultMonthFullyCopiedToStaging(ctx, db, defaultName, stagingName, timeCol, start, end)
	if err != nil {
		return copied, false, true, err
	}
	if !ready {
		return copied, false, true, nil
	}

	finalized, err := finalizeStagingPartition(
		ctx, db, table, defaultName, partitionName, stagingName, timeCol, start, end,
	)
	if err != nil {
		return copied, false, true, err
	}
	if !finalized {
		return copied, false, true, nil
	}
	return stagingRows, false, false, nil
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

func copyDefaultToStaging(
	ctx context.Context,
	db DB,
	defaultName, stagingName, timeCol string,
	start, end time.Time,
	batchSize int,
	rowBudget int64,
) (copied int64, budgetExhausted bool, err error) {
	if rowBudget <= 0 {
		return 0, true, nil
	}
	limit := batchSize
	if int64(limit) > rowBudget {
		limit = int(rowBudget)
	}
	// Copy-only: rows stay in DEFAULT until finalize deletes them in one transaction.
	insertQ := fmt.Sprintf(
		`INSERT INTO %s
		 SELECT d.*
		   FROM %s d
		  WHERE d.%s >= $1 AND d.%s < $2
		    AND NOT EXISTS (
		          SELECT 1 FROM %s s WHERE s.request_id = d.request_id
		        )
		  LIMIT $3`,
		pq.QuoteIdentifier(stagingName),
		pq.QuoteIdentifier(defaultName),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(stagingName),
	)
	res, err := db.ExecContext(ctx, insertQ, start, end, limit)
	if err != nil {
		return copied, false, fmt.Errorf("copy default into %s: %w", stagingName, err)
	}
	batch, err := res.RowsAffected()
	if err != nil {
		return copied, false, fmt.Errorf("inspect copy into %s: %w", stagingName, err)
	}
	if batch == 0 {
		return copied, false, nil
	}
	copied += batch
	rowBudget -= batch
	if rowBudget <= 0 {
		return copied, true, nil
	}
	if batch < int64(limit) {
		return copied, false, nil
	}
	extra, exhausted, err := copyDefaultToStaging(
		ctx, db, defaultName, stagingName, timeCol, start, end, batchSize, rowBudget,
	)
	return copied + extra, exhausted, err
}

func finalizeStagingPartition(
	ctx context.Context,
	db DB,
	table, defaultName, partitionName, stagingName, timeCol string,
	start, end time.Time,
) (bool, error) {
	stagingRows, err := countRowsInRange(ctx, db, stagingName, timeCol, start, end)
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

	var finalized bool
	err = execInTx(ctx, db, func(tx DB) error {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", qaRecordsRehomeLockKey); err != nil {
			return fmt.Errorf("acquire rehome finalize lock: %w", err)
		}
		if err := copyRemainingDefaultToStaging(ctx, tx, defaultName, stagingName, timeCol, start, end); err != nil {
			return err
		}
		ready, err := defaultMonthFullyCopiedToStaging(ctx, tx, defaultName, stagingName, timeCol, start, end)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("finalize %s: default month not fully copied to staging", partitionName)
		}
		stagingRows, err = countRowsInRange(ctx, tx, stagingName, timeCol, start, end)
		if err != nil {
			return err
		}
		if stagingRows == 0 {
			return nil
		}

		deleteQ := fmt.Sprintf(
			`DELETE FROM %s WHERE %s >= $1 AND %s < $2`,
			pq.QuoteIdentifier(defaultName),
			pq.QuoteIdentifier(timeCol),
			pq.QuoteIdentifier(timeCol),
		)
		if _, err := tx.ExecContext(ctx, deleteQ, start, end); err != nil {
			return fmt.Errorf("delete default month rows before attach: %w", err)
		}

		attached, err := childPartitionExists(ctx, tx, table, partitionName)
		if err != nil {
			return err
		}
		if !attached {
			createQ := fmt.Sprintf(
				"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
				pq.QuoteIdentifier(partitionName),
				pq.QuoteIdentifier(table),
				pq.QuoteLiteral(start.Format(time.RFC3339)),
				pq.QuoteLiteral(end.Format(time.RFC3339)),
			)
			if _, err := tx.ExecContext(ctx, createQ); err != nil {
				if !isOverlap(err) {
					return fmt.Errorf("create attached partition %s: %w", partitionName, err)
				}
			}
		}

		partitionRows, err := countRowsInRange(ctx, tx, partitionName, timeCol, start, end)
		if err != nil {
			return err
		}
		if partitionRows == 0 {
			insertQ := fmt.Sprintf(
				"INSERT INTO %s SELECT * FROM %s",
				pq.QuoteIdentifier(partitionName),
				pq.QuoteIdentifier(stagingName),
			)
			if _, err := tx.ExecContext(ctx, insertQ); err != nil {
				return fmt.Errorf("copy staging into %s: %w", partitionName, err)
			}
		}

		dropQ := "DROP TABLE IF EXISTS " + pq.QuoteIdentifier(stagingName)
		if _, err := tx.ExecContext(ctx, dropQ); err != nil {
			return fmt.Errorf("drop rehome staging %s: %w", stagingName, err)
		}
		finalized = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return finalized, nil
}

func copyRemainingDefaultToStaging(
	ctx context.Context,
	db DB,
	defaultName, stagingName, timeCol string,
	start, end time.Time,
) error {
	insertQ := fmt.Sprintf(
		`INSERT INTO %s
		 SELECT d.*
		   FROM %s d
		  WHERE d.%s >= $1 AND d.%s < $2
		    AND NOT EXISTS (
		          SELECT 1 FROM %s s WHERE s.request_id = d.request_id
		        )`,
		pq.QuoteIdentifier(stagingName),
		pq.QuoteIdentifier(defaultName),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(stagingName),
	)
	if _, err := db.ExecContext(ctx, insertQ, start, end); err != nil {
		return fmt.Errorf("sync default into %s before finalize: %w", stagingName, err)
	}
	return nil
}

func defaultMonthFullyCopiedToStaging(
	ctx context.Context,
	db DB,
	defaultName, stagingName, timeCol string,
	start, end time.Time,
) (bool, error) {
	q := fmt.Sprintf(
		`SELECT NOT EXISTS (
		    SELECT 1
		      FROM %s d
		     WHERE d.%s >= $1 AND d.%s < $2
		       AND NOT EXISTS (
		             SELECT 1 FROM %s s WHERE s.request_id = d.request_id
		           )
		  )`,
		pq.QuoteIdentifier(defaultName),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(timeCol),
		pq.QuoteIdentifier(stagingName),
	)
	var ready bool
	if err := db.QueryRowContext(ctx, q, start, end).Scan(&ready); err != nil {
		return false, fmt.Errorf("pgpartition: inspect rehome copy completeness: %w", err)
	}
	return ready, nil
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

// HasPendingRehome reports whether qa_records still has DEFAULT or staging rehome work.
func HasPendingRehome(ctx context.Context, db DB, table string) (bool, error) {
	if table != "qa_records" {
		return false, nil
	}
	defaultName, ok, err := defaultChildPartition(ctx, db, table)
	if err != nil {
		return false, err
	}
	if ok {
		remaining, err := countDefaultRows(ctx, db, defaultName)
		if err != nil {
			return false, err
		}
		if remaining > 0 {
			return true, nil
		}
	}
	stagingRows, err := countAllRehomeStagingRows(ctx, db, table)
	if err != nil {
		return false, err
	}
	return stagingRows > 0, nil
}
