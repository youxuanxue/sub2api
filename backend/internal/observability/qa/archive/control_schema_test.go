//go:build integration

package archive

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUS045_QAArchiveForwardCutoverMigrationFollowsBothTK071Migrations(t *testing.T) {
	entries, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	position := func(name string) int {
		t.Helper()
		for index, candidate := range names {
			if candidate == name {
				return index
			}
		}
		return -1
	}
	cutover := position("tk_072_qa_archive_forward_cutover.sql")
	require.NotEqual(t, -1, cutover)
	for _, predecessor := range []string{
		"tk_071_add_usage_logs_gateway_latency_ms.sql",
		"tk_071_vertex_embedding_model_mapping.sql",
	} {
		predecessorPosition := position(predecessor)
		require.NotEqual(t, -1, predecessorPosition)
		require.Less(t, predecessorPosition, cutover)
	}
}

func TestQAArchiveCloseoutControlMigrationIsAdditiveAndIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openArchiveIntegrationDB(t, "qa_archive_control")
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

func TestUS045_QAArchiveForwardCutoverMigrationIsAdditiveValidAndImmutable(t *testing.T) {
	ctx := context.Background()
	db := openArchiveIntegrationDB(t, "qa_archive_cutover")
	defer func() { _ = db.Close() }()

	applyMigration(t, ctx, db, "tk_069_create_qa_archive_shards.sql")
	applyMigration(t, ctx, db, "tk_070_qa_archive_closeout_control.sql")
	applyMigration(t, ctx, db, "tk_072_qa_archive_forward_cutover.sql")
	applyMigration(t, ctx, db, "tk_072_qa_archive_forward_cutover.sql")

	assertColumn(t, db, "qa_archive_shards", "forward_cutover", "boolean", false)
	assertColumnDefault(t, db, "qa_archive_shards", "forward_cutover", "false")
	assertForwardCutoverPartialUniqueIndex(t, db)

	approved := time.Date(2026, 8, 7, 21, 0, 0, 0, time.UTC)
	other := approved.Add(time.Hour)
	insertShard := func(start time.Time, state string, restoreVerified bool) int64 {
		t.Helper()
		var restoreAt any
		if restoreVerified {
			restoreAt = start.Add(90 * time.Minute)
		}
		var id int64
		require.NoError(t, db.QueryRowContext(ctx, `
INSERT INTO qa_archive_shards (window_start, window_end, state, restore_verified_at)
VALUES ($1, $2, $3, $4)
RETURNING id`, start, start.Add(time.Hour), state, restoreAt).Scan(&id))
		return id
	}

	pendingID := insertShard(approved.Add(-2*time.Hour), "pending", true)
	_, err := db.ExecContext(ctx, `UPDATE qa_archive_shards SET forward_cutover=true WHERE id=$1`, pendingID)
	require.Error(t, err, "pending shard must not become the cutover")

	unrestoredID := insertShard(approved.Add(-time.Hour), "committed", false)
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_shards SET forward_cutover=true WHERE id=$1`, unrestoredID)
	require.Error(t, err, "unrestored shard must not become the cutover")

	approvedID := insertShard(approved, "committed", true)
	otherID := insertShard(other, "committed", true)
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_shards SET forward_cutover=true WHERE id=$1`, approvedID)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_shards SET forward_cutover=true WHERE id=$1`, otherID)
	require.Error(t, err, "a second cutover must violate the partial unique index")
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_shards SET forward_cutover=false WHERE id=$1`, approvedID)
	require.Error(t, err, "the established cutover must not be unset")
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_shards SET window_start=window_start + interval '1 hour' WHERE id=$1`, approvedID)
	require.Error(t, err, "the established cutover must not be moved")
	_, err = db.ExecContext(ctx, `DELETE FROM qa_archive_shards WHERE id=$1`, approvedID)
	require.Error(t, err, "the established cutover must not be deleted")
}

func TestUS045_QAArchiveGapDecisionReceiptsAreAppendOnlyAndNotASecondStateMachine(t *testing.T) {
	ctx := context.Background()
	db := openArchiveIntegrationDB(t, "qa_archive_gap_receipts")
	defer func() { _ = db.Close() }()

	applyMigration(t, ctx, db, "tk_069_create_qa_archive_shards.sql")
	applyMigration(t, ctx, db, "tk_075_qa_archive_gap_decision_receipts.sql")
	assertTable(t, db, "qa_archive_gap_decision_receipts")
	assertTableAbsent(t, db, "qa_archive_gaps")

	planHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	planJSON := `{"schema_version":"qa-archive-gap-decision-v1","plan_hash":"` + planHash + `","windows":[{}]}`
	_, err := db.ExecContext(ctx, `
INSERT INTO qa_archive_gap_decision_receipts
    (plan_hash, plan_schema_version, plan_json, approved_by, window_count)
VALUES ($1,'qa-archive-gap-decision-v1',$2::jsonb,'feng',1)`, planHash, planJSON)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `UPDATE qa_archive_gap_decision_receipts SET approved_by='other' WHERE plan_hash=$1`, planHash)
	require.ErrorContains(t, err, "append-only")
	_, err = db.ExecContext(ctx, `DELETE FROM qa_archive_gap_decision_receipts WHERE plan_hash=$1`, planHash)
	require.ErrorContains(t, err, "append-only")
	_, err = db.ExecContext(ctx, `TRUNCATE qa_archive_gap_decision_receipts`)
	require.ErrorContains(t, err, "append-only")
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

func assertForwardCutoverPartialUniqueIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	var unique bool
	var predicate string
	require.NoError(t, db.QueryRow(`
SELECT i.indisunique, pg_get_expr(i.indpred, i.indrelid)
FROM pg_index i
JOIN pg_class idx ON idx.oid=i.indexrelid
WHERE idx.relname='idx_qa_archive_shards_one_forward_cutover'`).Scan(&unique, &predicate))
	require.True(t, unique)
	require.Equal(t, "forward_cutover", predicate)
}
