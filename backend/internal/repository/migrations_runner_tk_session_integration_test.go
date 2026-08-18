//go:build integration

package repository

import (
	"context"
	"database/sql"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestMigrationRunnerUTCSession_AsiaShanghaiFreshOpsChain(t *testing.T) {
	ctx := context.Background()
	const databaseName = "migration_runner_utc_session"

	_, err := integrationDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(databaseName)+" WITH (FORCE)")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(databaseName))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(databaseName)+" WITH (FORCE)")
	})

	dsn := migrationIntegrationDSN(t, databaseName, "Asia/Shanghai")
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(ctx))

	var backendPIDBefore int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPIDBefore))
	createFreshOpsMigrationFixtures(t, db)
	fsys := opsPartitionMigrationFS(t)
	require.NoError(t, applyMigrationsFS(ctx, db, fsys))

	var backendPIDAfter int
	var timezoneName string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPIDAfter))
	require.Equal(t, backendPIDBefore, backendPIDAfter,
		"successful migration cleanup must return the same healthy session to the pool")
	require.NoError(t, db.QueryRowContext(ctx, "SHOW TIME ZONE").Scan(&timezoneName))
	require.Equal(t, "Asia/Shanghai", timezoneName,
		"the migration connection must return to its application timezone")

	assertOpsCurrentWriterUTCAligned(t, db)
	assertMigrationRunnerOpsDailyCoverage(t, db)
	assertMigrationsRecorded(t, db,
		"tk_041_provision_ops_monthly_partitions.sql",
		"tk_080_ops_partition_utc_boundary_repair.sql",
		"tk_081_ops_daily_partition_cutover.sql",
	)
}

func TestMigrationRunnerUTCSession_RepairsAsiaShanghaiUpgradeOpsChain(t *testing.T) {
	ctx := context.Background()
	db := openMigrationIntegrationDatabase(t, "migration_runner_utc_upgrade")
	createFreshOpsMigrationFixtures(t, db)

	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	oldFS := migrationFilesFS(t,
		"tk_035_partition_ops_system_logs.sql",
		"tk_037_partition_ops_error_logs.sql",
		"tk_041_provision_ops_monthly_partitions.sql",
	)
	require.NoError(t, applyMigrationsSession(ctx, conn, oldFS),
		"the fixture must reproduce the pre-UTC migration runner in Asia/Shanghai")

	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		require.NoError(t, pgpartition.EnsureMonthly(ctx, conn, table, time.Now(), 3),
			"the old runtime owner must be able to create a canonical month beyond shifted children")
	}
	require.NoError(t, conn.Close())

	firstFutureMonth := nextUTCMonth(time.Now())
	shiftedUpper := firstFutureMonth.Add(-8 * time.Hour)
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		require.Equal(t, shiftedUpper, opsPartitionUpperBound(t, db, table, table+"_legacy"),
			"the upgrade fixture must contain the historical Asia/Shanghai catalog skew")
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO ops_system_logs (created_at, level, message)
		VALUES (now(), 'info', 'preserve-upgrade-current')`)
	require.NoError(t, err)
	preservedOIDs := opsCurrentWriterOIDs(t, db)
	preservedIndexOIDs := opsCurrentWriterIndexOIDs(t, db)
	require.NotEmpty(t, preservedIndexOIDs["ops_error_logs"])
	require.NotEmpty(t, preservedIndexOIDs["ops_system_logs"])

	currentFS := migrationFilesFS(t,
		"tk_080_ops_partition_utc_boundary_repair.sql",
		"tk_081_ops_daily_partition_cutover.sql",
	)
	require.NoError(t, applyMigrationsFS(ctx, db, currentFS))
	assertMigrationSessionTimezone(t, db, "Asia/Shanghai")
	assertOpsUpgradePreserved(t, db, preservedOIDs, preservedIndexOIDs)
	assertMigrationRunnerOpsDailyCoverage(t, db)
	assertMigrationsRecorded(t, db,
		"tk_080_ops_partition_utc_boundary_repair.sql",
		"tk_081_ops_daily_partition_cutover.sql",
	)
}

func TestMigrationRunnerUTCSession_RepairRejectsNonEmptyShiftedFutureAtomically(t *testing.T) {
	ctx := context.Background()
	db := openMigrationIntegrationDatabase(t, "migration_runner_utc_reject")
	createFreshOpsMigrationFixtures(t, db)

	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	require.NoError(t, applyMigrationsSession(ctx, conn, migrationFilesFS(t,
		"tk_035_partition_ops_system_logs.sql",
		"tk_037_partition_ops_error_logs.sql",
		"tk_041_provision_ops_monthly_partitions.sql",
	)))
	require.NoError(t, conn.Close())

	futureRowTime := nextUTCMonth(time.Now()).Add(14*24*time.Hour + 12*time.Hour)
	_, err = db.ExecContext(ctx, `
		INSERT INTO ops_error_logs (created_at)
		VALUES ($1)`, futureRowTime)
	require.NoError(t, err)

	before := migrationOpsPartitionSnapshot(t, db)
	err = applyMigrationsFS(ctx, db, migrationFilesFS(t,
		"tk_080_ops_partition_utc_boundary_repair.sql",
	))
	require.ErrorContains(t, err, "refusing to replace non-empty future child")
	assertMigrationSessionTimezone(t, db, "Asia/Shanghai")
	require.Equal(t, before, migrationOpsPartitionSnapshot(t, db),
		"a rejected repair must roll back every catalog change")
	assertMigrationsNotRecorded(t, db, "tk_080_ops_partition_utc_boundary_repair.sql")

	var repairChecks int
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conname LIKE '%_tk_utc_bound_check'`).Scan(&repairChecks))
	require.Zero(t, repairChecks, "a rejected repair must not leave validation constraints")
}

func TestMigrationRunnerUTCSession_RepairIsStrictNoopAfterDailyCutover(t *testing.T) {
	ctx := context.Background()
	db := openMigrationIntegrationDatabase(t, "migration_runner_utc_noop")
	createFreshOpsMigrationFixtures(t, db)
	require.NoError(t, applyMigrationsFS(ctx, db, opsPartitionMigrationFS(t)))

	before := migrationOpsPartitionSnapshot(t, db)
	repairSQL, err := dbmigrations.FS.ReadFile("tk_080_ops_partition_utc_boundary_repair.sql")
	require.NoError(t, err)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(repairSQL))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	require.Equal(t, before, migrationOpsPartitionSnapshot(t, db),
		"replaying the repair after tk_081 must retain every child OID and bound")
	assertMigrationSessionTimezone(t, db, "Asia/Shanghai")
}

func openMigrationIntegrationDatabase(t *testing.T, databaseName string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(databaseName)+" WITH (FORCE)")
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "CREATE DATABASE "+pq.QuoteIdentifier(databaseName))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+pq.QuoteIdentifier(databaseName)+" WITH (FORCE)")
	})

	db, err := sql.Open("postgres", migrationIntegrationDSN(t, databaseName, "Asia/Shanghai"))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(ctx))
	return db
}

func migrationFilesFS(t *testing.T, names ...string) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range names {
		content, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		fsys[name] = &fstest.MapFile{Data: content}
	}
	return fsys
}

func nextUTCMonth(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}

func opsPartitionUpperBound(t *testing.T, db *sql.DB, parentName, childName string) time.Time {
	t.Helper()
	var upper time.Time
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT substring(pg_get_expr(child.relpartbound, child.oid, true)
		                 FROM $$TO \('([^']+)'$$)::timestamptz
		FROM pg_inherits inheritance
		JOIN pg_class child ON child.oid = inheritance.inhrelid
		WHERE inheritance.inhparent = to_regclass($1)
		  AND child.relname = $2`, parentName, childName).Scan(&upper))
	return upper.UTC()
}

func opsCurrentWriterOIDs(t *testing.T, db *sql.DB) map[string]uint32 {
	t.Helper()
	result := make(map[string]uint32, 2)
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		var oid uint32
		require.NoError(t, db.QueryRowContext(context.Background(), `
			SELECT child.oid
			FROM pg_inherits inheritance
			JOIN pg_class child ON child.oid = inheritance.inhrelid
			WHERE inheritance.inhparent = to_regclass($1)
			  AND child.relname = $2`, table, table+"_legacy").Scan(&oid))
		result[table] = oid
	}
	return result
}

func opsCurrentWriterIndexOIDs(t *testing.T, db *sql.DB) map[string][]uint32 {
	t.Helper()
	result := make(map[string][]uint32, 2)
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		rows, err := db.QueryContext(context.Background(), `
			SELECT child_index.oid
			FROM pg_index child_index_meta
			JOIN pg_class child_index ON child_index.oid = child_index_meta.indexrelid
			JOIN pg_inherits index_inheritance
			  ON index_inheritance.inhrelid = child_index_meta.indexrelid
			WHERE child_index_meta.indrelid = $1::regclass
			ORDER BY child_index.oid`, table+"_legacy")
		require.NoError(t, err)
		var indexOIDs []uint32
		for rows.Next() {
			var oid uint32
			require.NoError(t, rows.Scan(&oid))
			indexOIDs = append(indexOIDs, oid)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		result[table] = indexOIDs
	}
	return result
}

func assertOpsUpgradePreserved(
	t *testing.T,
	db *sql.DB,
	writerOIDs map[string]uint32,
	indexOIDs map[string][]uint32,
) {
	t.Helper()
	require.Equal(t, writerOIDs, opsCurrentWriterOIDs(t, db),
		"the repair must preserve current writer relation OIDs")
	require.Equal(t, indexOIDs, opsCurrentWriterIndexOIDs(t, db),
		"the repair must preserve current writer index OIDs and inheritance")
	assertOpsCurrentWriterUTCAligned(t, db)

	var preservedRows int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM ops_system_logs_legacy
		WHERE message = 'preserve-upgrade-current'`).Scan(&preservedRows))
	require.Equal(t, 1, preservedRows)
}

func assertOpsCurrentWriterUTCAligned(t *testing.T, db *sql.DB) {
	t.Helper()
	expectedUpper := nextUTCMonth(time.Now())
	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		require.Equal(t, expectedUpper, opsPartitionUpperBound(t, db, table, table+"_legacy"),
			"%s legacy upper bound must be the next UTC month boundary", table)
	}
}

func assertMigrationRunnerOpsDailyCoverage(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	firstFutureMonth := nextUTCMonth(time.Now())
	horizonEnd := firstFutureMonth.AddDate(0, 3, 0)
	expectedDays := int(horizonEnd.Sub(firstFutureMonth).Hours() / 24)

	for _, table := range []string{"ops_error_logs", "ops_system_logs"} {
		var dailyChildren int
		require.NoError(t, db.QueryRowContext(ctx, `
			WITH bounds AS (
			  SELECT child.oid,
			         child.relname,
			         substring(pg_get_expr(child.relpartbound, child.oid, true)
			                   FROM $$FROM \('([^']+)'$$)::timestamptz AS lower_bound,
			         substring(pg_get_expr(child.relpartbound, child.oid, true)
			                   FROM $$TO \('([^']+)'$$)::timestamptz AS upper_bound
			  FROM pg_inherits inheritance
			  JOIN pg_class child ON child.oid = inheritance.inhrelid
			  WHERE inheritance.inhparent = to_regclass($1)
			)
			SELECT count(*)
			FROM bounds
			WHERE lower_bound >= $2
			  AND upper_bound <= $3
			  AND upper_bound = lower_bound + interval '1 day'
			  AND relname = $4 || '_' || to_char(lower_bound AT TIME ZONE 'UTC', 'YYYYMMDD')`,
			table, firstFutureMonth, horizonEnd, table).Scan(&dailyChildren))
		require.Equal(t, expectedDays, dailyChildren,
			"%s must have complete UTC daily coverage for the three-month horizon", table)

		var nonDailyChildren int
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_inherits inheritance
			JOIN pg_class child ON child.oid = inheritance.inhrelid
			WHERE inheritance.inhparent = to_regclass($1)
			  AND substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$FROM \('([^']+)'$$)::timestamptz < $3
			  AND substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$TO \('([^']+)'$$)::timestamptz > $2
			  AND substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$TO \('([^']+)'$$)::timestamptz <>
			      substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$FROM \('([^']+)'$$)::timestamptz + interval '1 day'`,
			table, firstFutureMonth, horizonEnd).Scan(&nonDailyChildren))
		require.Zero(t, nonDailyChildren,
			"%s must not retain monthly or shifted coverage inside the daily horizon", table)

		var missingIndexChildren int
		require.NoError(t, db.QueryRowContext(ctx, `
			SELECT count(*)
			FROM pg_inherits table_inheritance
			JOIN pg_class child ON child.oid = table_inheritance.inhrelid
			WHERE table_inheritance.inhparent = to_regclass($1)
			  AND substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$FROM \('([^']+)'$$)::timestamptz >= $2
			  AND substring(pg_get_expr(child.relpartbound, child.oid, true)
			                FROM $$TO \('([^']+)'$$)::timestamptz <= $3
			  AND NOT EXISTS (
			    SELECT 1
			    FROM pg_index child_index
			    WHERE child_index.indrelid = child.oid
			  )`, table, firstFutureMonth, horizonEnd).Scan(&missingIndexChildren))
		require.Zero(t, missingIndexChildren,
			"%s daily children must inherit indexes", table)
	}
}

func migrationOpsPartitionSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT parent.relname || '/' || child.relname,
		       child.oid::text || '|' || pg_get_expr(child.relpartbound, child.oid, true) || '|' ||
		       COALESCE((
		         SELECT string_agg(index_child.oid::text, ',' ORDER BY index_child.oid)
		         FROM pg_index child_index_meta
		         JOIN pg_class index_child ON index_child.oid = child_index_meta.indexrelid
		         JOIN pg_inherits index_inheritance
		           ON index_inheritance.inhrelid = child_index_meta.indexrelid
		         WHERE child_index_meta.indrelid = child.oid
		       ), '')
		FROM pg_inherits inheritance
		JOIN pg_class parent ON parent.oid = inheritance.inhparent
		JOIN pg_class child ON child.oid = inheritance.inhrelid
		WHERE parent.relname IN ('ops_error_logs', 'ops_system_logs')
		ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var name string
		var state string
		require.NoError(t, rows.Scan(&name, &state))
		result[name] = state
	}
	require.NoError(t, rows.Err())
	return result
}

func assertMigrationSessionTimezone(t *testing.T, db *sql.DB, expected string) {
	t.Helper()
	var timezoneName string
	require.NoError(t, db.QueryRowContext(context.Background(), "SHOW TIME ZONE").Scan(&timezoneName))
	require.Equal(t, expected, timezoneName)
}

func assertMigrationsRecorded(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	var recorded int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM schema_migrations
		WHERE filename = ANY($1)`, pq.Array(names)).Scan(&recorded))
	require.Equal(t, len(names), recorded)
}

func assertMigrationsNotRecorded(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	var recorded int
	require.NoError(t, db.QueryRowContext(context.Background(), `
		SELECT count(*)
		FROM schema_migrations
		WHERE filename = ANY($1)`, pq.Array(names)).Scan(&recorded))
	require.Zero(t, recorded)
}

func migrationIntegrationDSN(t *testing.T, databaseName, timezoneName string) string {
	t.Helper()
	parsed, err := url.Parse(integrationPostgresDSN)
	require.NoError(t, err)
	parsed.Path = "/" + databaseName
	query := parsed.Query()
	query.Set("TimeZone", timezoneName)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func createFreshOpsMigrationFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	systemSQL, err := dbmigrations.FS.ReadFile("054_ops_system_logs.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(systemSQL))
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE ops_error_logs (
			id BIGSERIAL PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_ops_error_logs_created_at ON ops_error_logs (created_at DESC);
	`)
	require.NoError(t, err)
}

func opsPartitionMigrationFS(t *testing.T) fstest.MapFS {
	t.Helper()
	fsys := fstest.MapFS{}
	for _, name := range []string{
		"tk_035_partition_ops_system_logs.sql",
		"tk_037_partition_ops_error_logs.sql",
		"tk_041_provision_ops_monthly_partitions.sql",
		"tk_080_ops_partition_utc_boundary_repair.sql",
		"tk_081_ops_daily_partition_cutover.sql",
	} {
		content, err := dbmigrations.FS.ReadFile(name)
		require.NoError(t, err)
		fsys[name] = &fstest.MapFile{Data: content}
	}
	return fsys
}
