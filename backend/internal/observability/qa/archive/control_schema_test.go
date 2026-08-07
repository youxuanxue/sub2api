//go:build integration

package archive_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestQAArchiveCloseoutControlMigrationIsAdditiveAndIdempotent(t *testing.T) {
	ctx := context.Background()
	container, err := postgres.Run(
		ctx,
		"postgres:18.1-alpine3.23",
		postgres.WithDatabase("qa_archive_control"),
		postgres.WithUsername("postgres"),
		postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("start postgres: %v", err)
	}
	defer func() { _ = container.Terminate(ctx) }()

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	require.NoError(t, err)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	applyMigration(t, ctx, db, "tk_069_create_qa_archive_shards.sql")
	applyMigration(t, ctx, db, "tk_070_qa_archive_closeout_control.sql")
	applyMigration(t, ctx, db, "tk_070_qa_archive_closeout_control.sql")

	assertColumn(t, db, "qa_archive_shards", "commit_etag", "text", true)
	assertColumn(t, db, "qa_archive_shards", "aggregate_record_count", "bigint", false)
	assertColumn(t, db, "qa_archive_shards", "verified_at", "timestamp with time zone", true)
	assertColumn(t, db, "qa_archive_shards", "restore_verified_at", "timestamp with time zone", true)
	assertColumn(t, db, "qa_archive_shards", "verification_error_code", "text", true)
	assertColumn(t, db, "qa_archive_shards", "cleanup_eligible", "boolean", false)
	assertColumnDefault(t, db, "qa_archive_shards", "cleanup_eligible", "false")

	assertTable(t, db, "qa_archive_segments")
	assertTable(t, db, "qa_archive_segment_records")
	assertUniqueColumns(t, db, "qa_archive_segments", "shard_id", "segment_id")
	assertUniqueColumns(t, db, "qa_archive_segment_records", "segment_id", "created_at", "request_id")
	assertForeignKeyDeleteAction(t, db, "qa_archive_segments", "shard_id", "qa_archive_shards", "NO ACTION")
	assertForeignKeyDeleteAction(t, db, "qa_archive_segment_records", "segment_id", "qa_archive_segments", "CASCADE")

	assertTableAbsent(t, db, "qa_archive_gaps")
	assertTableAbsent(t, db, "qa_cleanup_receipts")
}

func applyMigration(t *testing.T, ctx context.Context, db *sql.DB, name string) {
	t.Helper()
	body, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(body))
	require.NoError(t, err)
}

func assertTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
	require.True(t, exists, "expected table %s", table)
}

func assertTableAbsent(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	require.NoError(t, db.QueryRow(`SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists))
	require.False(t, exists, "did not expect table %s", table)
}

func assertColumn(t *testing.T, db *sql.DB, table, column, dataType string, nullable bool) {
	t.Helper()
	var gotType, gotNullable string
	require.NoError(t, db.QueryRow(`
SELECT data_type, is_nullable
FROM information_schema.columns
WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, table, column).Scan(&gotType, &gotNullable))
	require.Equal(t, dataType, gotType)
	if nullable {
		require.Equal(t, "YES", gotNullable)
	} else {
		require.Equal(t, "NO", gotNullable)
	}
}

func assertColumnDefault(t *testing.T, db *sql.DB, table, column, want string) {
	t.Helper()
	var got sql.NullString
	require.NoError(t, db.QueryRow(`
SELECT column_default
FROM information_schema.columns
WHERE table_schema='public' AND table_name=$1 AND column_name=$2`, table, column).Scan(&got))
	require.True(t, got.Valid)
	require.Equal(t, want, got.String)
}

func assertUniqueColumns(t *testing.T, db *sql.DB, table string, columns ...string) {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`
SELECT count(*)
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid=c.conrelid
WHERE tbl.relname=$1 AND c.contype IN ('p','u')
  AND ARRAY(
    SELECT a.attname::text
    FROM unnest(c.conkey) WITH ORDINALITY k(attnum, ord)
    JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum
    ORDER BY k.ord
  )=$2::text[]`, table, pq.Array(columns)).Scan(&count))
	require.Equal(t, 1, count, "expected unique columns %v on %s", columns, table)
}

func assertForeignKeyDeleteAction(t *testing.T, db *sql.DB, table, column, refTable, want string) {
	t.Helper()
	var got string
	require.NoError(t, db.QueryRow(`
SELECT CASE c.confdeltype WHEN 'a' THEN 'NO ACTION' WHEN 'r' THEN 'RESTRICT'
       WHEN 'c' THEN 'CASCADE' WHEN 'n' THEN 'SET NULL' WHEN 'd' THEN 'SET DEFAULT' END
FROM pg_constraint c
JOIN pg_class tbl ON tbl.oid=c.conrelid
JOIN pg_class ref ON ref.oid=c.confrelid
JOIN pg_attribute a ON a.attrelid=tbl.oid AND a.attnum=ANY(c.conkey)
WHERE c.contype='f' AND tbl.relname=$1 AND a.attname=$2 AND ref.relname=$3`, table, column, refTable).Scan(&got))
	require.Equal(t, want, got)
}
