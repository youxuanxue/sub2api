//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

var opsDailyCutoverNow = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

func TestOpsDailyPartitionMigration_SuccessFreshAndIdempotent(t *testing.T) {
	ctx := context.Background()
	schema := setupOpsDailyMigrationSchema(t, "ops_daily_success", 2)
	migrationSQL := readOpsDailyMigration(t)

	var currentOID uint32
	require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT %s::regclass::oid`, pq.QuoteLiteral(schema+".ops_system_logs_legacy"))).Scan(&currentOID))
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s.ops_system_logs (created_at, level, message)
		VALUES ('2026-08-13T12:00:00Z', 'info', 'preserve-current')`, pq.QuoteIdentifier(schema)))
	require.NoError(t, err)

	runOpsDailyMigration(t, schema, migrationSQL)
	assertOpsDailyMigrationResult(t, schema, currentOID)
	beforeCompatibilityCheck := partitionOIDSnapshot(t, schema)
	assertPreviousMonthlyOwnerCompatibility(t, schema)
	require.Equal(t, beforeCompatibilityCheck, partitionOIDSnapshot(t, schema),
		"the previous monthly owner compatibility check must leave the daily topology unchanged")

	before := partitionOIDSnapshot(t, schema)
	runOpsDailyMigration(t, schema, migrationSQL)
	require.Equal(t, before, partitionOIDSnapshot(t, schema), "an idempotent rerun must not replace daily children")
}

func TestOpsDailyPartitionMigration_RejectsUnsafeTopologyAtomically(t *testing.T) {
	migrationSQL := readOpsDailyMigration(t)

	t.Run("non-empty future monthly", func(t *testing.T) {
		ctx := context.Background()
		schema := setupOpsDailyMigrationSchema(t, "ops_daily_nonempty", 3)
		qSchema := pq.QuoteIdentifier(schema)
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s.ops_error_logs (created_at, error_phase, error_type)
			VALUES ('2026-09-15T00:00:00Z', 'test', 'test')`, qSchema))
		require.NoError(t, err)

		before := partitionOIDSnapshot(t, schema)
		err = execOpsDailyMigration(ctx, schema, migrationSQL)
		require.ErrorContains(t, err, "refusing to replace non-empty future child")
		require.Equal(t, before, partitionOIDSnapshot(t, schema), "the failed migration must leave both parents unchanged")
		assertNoUnexpectedDailyTables(t, schema)
	})

	t.Run("partial daily month", func(t *testing.T) {
		ctx := context.Background()
		schema := setupOpsDailyMigrationSchema(t, "ops_daily_partial", 0)
		qSchema := pq.QuoteIdentifier(schema)
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
			CREATE TABLE %s.ops_system_logs_20260901
			PARTITION OF %s.ops_system_logs
			FOR VALUES FROM ('2026-09-01T00:00:00Z') TO ('2026-09-02T00:00:00Z')`, qSchema, qSchema))
		require.NoError(t, err)

		before := partitionOIDSnapshot(t, schema)
		err = execOpsDailyMigration(ctx, schema, migrationSQL)
		require.ErrorContains(t, err, "partial or unexpected coverage")
		require.Equal(t, before, partitionOIDSnapshot(t, schema))
		assertNoUnexpectedDailyTables(t, schema)
	})

	t.Run("default partition", func(t *testing.T) {
		ctx := context.Background()
		schema := setupOpsDailyMigrationSchema(t, "ops_daily_default", 0)
		qSchema := pq.QuoteIdentifier(schema)
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
			`CREATE TABLE %s.ops_error_logs_default PARTITION OF %s.ops_error_logs DEFAULT`, qSchema, qSchema))
		require.NoError(t, err)

		before := partitionOIDSnapshot(t, schema)
		err = execOpsDailyMigration(ctx, schema, migrationSQL)
		require.ErrorContains(t, err, "DEFAULT, MAXVALUE, or unparseable child bounds")
		require.Equal(t, before, partitionOIDSnapshot(t, schema))
		assertNoUnexpectedDailyTables(t, schema)
	})
}

func TestOpsDailyPartitionMigration_LockContract(t *testing.T) {
	migrationSQL := readOpsDailyMigration(t)

	t.Run("current writer times out atomically under required parent lock", func(t *testing.T) {
		ctx := context.Background()
		schema := setupOpsDailyMigrationSchema(t, "ops_daily_current_dml", 3)
		holder, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		_, err = holder.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO %s.ops_system_logs (created_at, level, message)
			VALUES ('2026-08-13T12:00:00Z', 'info', 'held-current-write')`, pq.QuoteIdentifier(schema)))
		require.NoError(t, err)

		before := partitionOIDSnapshot(t, schema)
		err = execOpsDailyMigration(ctx, schema, migrationSQL)
		require.Error(t, err)
		require.Contains(t, strings.ToLower(err.Error()), "lock timeout")
		require.Equal(t, before, partitionOIDSnapshot(t, schema),
			"PostgreSQL DROP PARTITION conflicts with a parent writer, so timeout must preserve the old topology")
		assertNoUnexpectedDailyTables(t, schema)
	})

	t.Run("future child reader fails closed", func(t *testing.T) {
		ctx := context.Background()
		schema := setupOpsDailyMigrationSchema(t, "ops_daily_future_lock", 3)
		holder, err := integrationDB.BeginTx(ctx, nil)
		require.NoError(t, err)
		defer func() { _ = holder.Rollback() }()
		_, err = holder.ExecContext(ctx, fmt.Sprintf(
			`SELECT 1 FROM %s.ops_error_logs_202609 LIMIT 1`, pq.QuoteIdentifier(schema)))
		require.NoError(t, err)

		before := partitionOIDSnapshot(t, schema)
		started := time.Now()
		err = execOpsDailyMigration(ctx, schema, migrationSQL)
		require.Error(t, err)
		require.Contains(t, strings.ToLower(err.Error()), "lock timeout")
		require.Less(t, time.Since(started), 10*time.Second)
		require.Equal(t, before, partitionOIDSnapshot(t, schema))
		assertNoUnexpectedDailyTables(t, schema)
	})
}

func setupOpsDailyMigrationSchema(t *testing.T, schema string, futureMonths int) string {
	t.Helper()
	ctx := context.Background()
	qSchema := pq.QuoteIdentifier(schema)
	_, err := integrationDB.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+qSchema+" CASCADE")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "CREATE SCHEMA "+qSchema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+qSchema+" CASCADE")
	})

	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		columns := "id BIGSERIAL, created_at TIMESTAMPTZ NOT NULL"
		if table == "ops_error_logs" {
			columns += ", error_phase TEXT, error_type TEXT"
		} else {
			columns += ", level TEXT, message TEXT"
		}
		_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(`
			CREATE TABLE %s.%s (%s, PRIMARY KEY (id, created_at)) PARTITION BY RANGE (created_at);
			CREATE INDEX %s ON %s.%s (created_at DESC);
			CREATE TABLE %s.%s PARTITION OF %s.%s
			FOR VALUES FROM (MINVALUE) TO ('2026-09-01T00:00:00Z');`,
			qSchema, pq.QuoteIdentifier(table), columns,
			pq.QuoteIdentifier("idx_"+table+"_created_at"), qSchema, pq.QuoteIdentifier(table),
			qSchema, pq.QuoteIdentifier(table+"_legacy"), qSchema, pq.QuoteIdentifier(table)))
		require.NoError(t, err)

		monthStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		for month := 0; month < futureMonths; month++ {
			start := monthStart.AddDate(0, month, 0)
			end := start.AddDate(0, 1, 0)
			name := table + "_" + start.Format("200601")
			_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
				`CREATE TABLE %s.%s PARTITION OF %s.%s FOR VALUES FROM (%s) TO (%s)`,
				qSchema, pq.QuoteIdentifier(name), qSchema, pq.QuoteIdentifier(table),
				pq.QuoteLiteral(start.Format(time.RFC3339)), pq.QuoteLiteral(end.Format(time.RFC3339))))
			require.NoError(t, err)
		}
	}
	return schema
}

func readOpsDailyMigration(t *testing.T) string {
	t.Helper()
	migrationSQL, err := dbmigrations.FS.ReadFile("tk_081_ops_daily_partition_cutover.sql")
	require.NoError(t, err)
	return string(migrationSQL)
}

func runOpsDailyMigration(t *testing.T, schema, migrationSQL string) {
	t.Helper()
	require.NoError(t, execOpsDailyMigration(context.Background(), schema, migrationSQL))
}

func execOpsDailyMigration(ctx context.Context, schema, migrationSQL string) error {
	tx, err := integrationDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "SET LOCAL search_path = "+pq.QuoteIdentifier(schema)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "SET LOCAL tokenkey.ops_daily_cutover_now = "+pq.QuoteLiteral(opsDailyCutoverNow.Format(time.RFC3339))); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, migrationSQL); err != nil {
		return err
	}
	return tx.Commit()
}

func assertOpsDailyMigrationResult(t *testing.T, schema string, currentOID uint32) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		var preservedOID uint32
		require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT %s::regclass::oid`, pq.QuoteLiteral(schema+"."+table+"_legacy"))).Scan(&preservedOID))
		if table == "ops_system_logs" {
			require.Equal(t, currentOID, preservedOID)
			var rows int
			require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(
				`SELECT count(*) FROM %s.ops_system_logs_legacy WHERE message = 'preserve-current'`, pq.QuoteIdentifier(schema))).Scan(&rows))
			require.Equal(t, 1, rows)
		}

		var dailyChildren int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_inherits inheritance
			JOIN pg_class child ON child.oid = inheritance.inhrelid
			JOIN pg_namespace namespace ON namespace.oid = child.relnamespace
			WHERE inheritance.inhparent = to_regclass($1)
			  AND namespace.nspname = $2
			  AND child.relname ~ $3`, schema+"."+table, schema, "^"+table+"_2026(09|10|11)[0-9]{2}$").Scan(&dailyChildren))
		require.Equal(t, 91, dailyChildren)

		var missingIndexChildren int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_inherits table_inheritance
			JOIN pg_class child ON child.oid = table_inheritance.inhrelid
			WHERE table_inheritance.inhparent = to_regclass($1)
			  AND child.relname ~ $2
			  AND NOT EXISTS (
				SELECT 1 FROM pg_index child_index WHERE child_index.indrelid = child.oid
			  )`, schema+"."+table, "^"+table+"_2026(09|10|11)[0-9]{2}$").Scan(&missingIndexChildren))
		require.Zero(t, missingIndexChildren)
	}

	qSchema := pq.QuoteIdentifier(schema)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s.ops_system_logs (created_at, level, message)
		VALUES ('2026-09-01T00:00:00Z', 'info', 'daily-boundary')`, qSchema))
	require.NoError(t, err)
	var routedRows int
	require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT count(*) FROM %s.ops_system_logs_20260901 WHERE message = 'daily-boundary'`, qSchema)).Scan(&routedRows))
	require.Equal(t, 1, routedRows)
}

func assertPreviousMonthlyOwnerCompatibility(t *testing.T, schema string) {
	t.Helper()
	ctx := context.Background()
	qSchema := pq.QuoteIdentifier(schema)
	conn, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "SET search_path = "+qSchema)
	require.NoError(t, err)
	defer func() { _, _ = conn.ExecContext(context.Background(), "RESET search_path") }()

	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		qualifiedTable := qSchema + "." + pq.QuoteIdentifier(table)
		require.NoError(t, pgpartition.EnsureMonthly(ctx, conn, table, opsDailyCutoverNow, 3),
			"the previous monthly owner must treat complete daily month coverage as benign overlap")

		var covered int
		require.NoError(t, conn.QueryRowContext(ctx, `
			WITH child_bounds AS (
			  SELECT pg_get_expr(child.relpartbound, child.oid, true) AS bound_expr
			  FROM pg_inherits inheritance
			  JOIN pg_class child ON child.oid = inheritance.inhrelid
			  WHERE inheritance.inhparent = to_regclass($1)
			), parsed_bounds AS (
			  SELECT
			    bound_expr LIKE 'FOR VALUES FROM (MINVALUE)%' AS lower_unbounded,
			    substring(bound_expr FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
			    substring(bound_expr FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
			  FROM child_bounds
			), covered_union AS (
			  SELECT range_agg(tstzrange(
			    CASE WHEN lower_unbounded THEN NULL ELSE lower_bound END,
			    upper_bound,
			    '[)'
			  )) AS ranges
			  FROM parsed_bounds
			  WHERE (lower_unbounded OR lower_bound IS NOT NULL) AND upper_bound IS NOT NULL
			), required_ranges AS (
			  SELECT month_start, month_start + interval '1 month' AS month_end
			  FROM generate_series(
			    date_trunc('month', $2::timestamptz),
			    date_trunc('month', $2::timestamptz) + interval '3 months',
			    interval '1 month'
			  ) AS month_start
			)
			SELECT count(*)
			FROM required_ranges, covered_union
			WHERE covered_union.ranges @> tstzrange(month_start, month_end, '[)')`,
			qualifiedTable, opsDailyCutoverNow).Scan(&covered))
		require.Equal(t, 4, covered,
			"the previous owner must prove current month plus three complete future months")
	}
}

func partitionOIDSnapshot(t *testing.T, schema string) map[string]uint32 {
	t.Helper()
	rows, err := integrationDB.QueryContext(context.Background(), `
		SELECT parent.relname || '/' || child.relname, child.oid
		FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid = inheritance.inhparent
		JOIN pg_namespace parent_namespace ON parent_namespace.oid = parent.relnamespace
		JOIN pg_class child ON child.oid = inheritance.inhrelid
		WHERE parent_namespace.nspname = $1
		  AND parent.relname IN ('ops_error_logs', 'ops_system_logs')
		ORDER BY 1`, schema)
	require.NoError(t, err)
	defer rows.Close()

	result := make(map[string]uint32)
	for rows.Next() {
		var name string
		var oid uint32
		require.NoError(t, rows.Scan(&name, &oid))
		result[name] = oid
	}
	require.NoError(t, rows.Err())
	return result
}

func assertNoUnexpectedDailyTables(t *testing.T, schema string) {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM pg_class relation
		JOIN pg_namespace namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = $1
		  AND relation.relname ~ '^ops_(error|system)_logs_2026(09|10|11)[0-9]{2}$'
		  AND NOT EXISTS (
			SELECT 1 FROM pg_inherits inheritance WHERE inheritance.inhrelid = relation.oid
		  )`, schema).Scan(&count))
	require.Zero(t, count, "failed migration must not leave detached staging relations")
}
