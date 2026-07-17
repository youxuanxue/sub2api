//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TestPgPartition_OpsSystemLogsConvertedByMigration proves tk_035 converts
// ops_system_logs to a partitioned table when applied in the REAL migration sequence
// (054 creates it plain; tk_035 converts it). The harness applies all migrations in
// TestMain, so a partitioned ops_system_logs here means the conversion DDL is valid
// end-to-end against a real Postgres.
func TestPgPartition_OpsSystemLogsConvertedByMigration(t *testing.T) {
	ctx := context.Background()
	ok, err := pgpartition.IsPartitioned(ctx, integrationDB, "ops_system_logs")
	require.NoError(t, err)
	require.True(t, ok, "tk_035 must convert ops_system_logs to a partitioned table")

	// Regression (review R-001): the id sequence must be OWNED BY the parent, not the
	// legacy partition. Otherwise retention dropping the legacy partition either fails
	// (plain DROP refused) or, with CASCADE, drops the sequence and breaks id generation.
	// (tk_035 step 2a reassigns ownership.)
	var seqOwner string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT t.relname FROM pg_depend d
		JOIN pg_class s ON s.oid = d.objid AND s.relname = 'ops_system_logs_id_seq'
		JOIN pg_class t ON t.oid = d.refobjid
		WHERE d.deptype = 'a'`).Scan(&seqOwner))
	require.Equal(t, "ops_system_logs", seqOwner,
		"id sequence must be owned by the parent so the legacy partition drops cleanly")

	// A row inserted without id (mirrors BatchInsertSystemLogs COPY) must route into a
	// partition and get an auto id from the inherited sequence.
	var id int64
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO ops_system_logs(created_at, level, message) VALUES (now(), 'info', 'pgpart-itest') RETURNING id`,
	).Scan(&id))
	require.Positive(t, id)
	_, err = integrationDB.ExecContext(ctx, `DELETE FROM ops_system_logs WHERE message = 'pgpart-itest'`)
	require.NoError(t, err)
}

func TestCreatePartitionedIndexConcurrently_AttachesEveryPartitionAndRetries(t *testing.T) {
	ctx := context.Background()
	table := "pgpart_itest_online_index"
	parentIndex := "idx_pgpart_itest_online_host_created"
	qTable := pq.QuoteIdentifier(table)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	})

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE %s (host TEXT, created_at TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (created_at);
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
		INSERT INTO %s VALUES ('edge-a', '2026-01-15'), ('edge-b', '2026-02-15');`,
		qTable,
		pq.QuoteIdentifier(table+"_p1"), qTable,
		pq.QuoteIdentifier(table+"_p2"), qTable,
		qTable,
	))
	require.NoError(t, err)

	policy := nonTransactionalIndexPolicy{
		indexName:            parentIndex,
		partitionedTable:     table,
		partitionedIndexExpr: "host, created_at DESC",
	}
	require.NoError(t, createPartitionedIndexConcurrently(ctx, integrationDB, policy))
	// A second run covers interrupted rollout retries after all child indexes are attached.
	require.NoError(t, createPartitionedIndexConcurrently(ctx, integrationDB, policy))

	var valid bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT i.indisvalid
		FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = $1`, parentIndex).Scan(&valid))
	require.True(t, valid)

	var tablePartitions, indexPartitions int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class parent ON parent.oid = i.inhparent
		WHERE parent.relname = $1`, table).Scan(&tablePartitions))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class parent ON parent.oid = i.inhparent
		WHERE parent.relname = $1`, parentIndex).Scan(&indexPartitions))
	require.Equal(t, tablePartitions, indexPartitions)
}

// TestPgPartition_EnsureMonthlySkipsLegacyOverlap mirrors the post-conversion state: a
// wide legacy partition covers everything up to next month, so EnsureMonthly's current
// month overlaps it (42P17) and must be skipped while future months are still created.
func TestPgPartition_EnsureMonthlySkipsLegacyOverlap(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_ensure"
	q := pq.QuoteIdentifier(tbl)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+q)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+q) })

	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := thisMonth.AddDate(0, 1, 0)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s (id BIGSERIAL, created_at TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (created_at)", q))
	require.NoError(t, err)
	// legacy partition covering [MINVALUE, nextMonth) -> includes the current month.
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (MINVALUE) TO (%s)",
		pq.QuoteIdentifier(tbl+"_legacy"), q, pq.QuoteLiteral(nextMonth.Format("2006-01-02"))))
	require.NoError(t, err)

	// EnsureMonthly: current month overlaps legacy (skipped); next + next+1 created.
	require.NoError(t, pgpartition.EnsureMonthly(ctx, integrationDB, tbl, now, 2))

	var parts int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid=i.inhrelid
		JOIN pg_class p ON p.oid=i.inhparent WHERE p.relname=$1`, tbl).Scan(&parts))
	require.Equal(t, 3, parts, "legacy + 2 future months (current month skipped as overlap)")
}

// TestPgPartition_DropExpiredByData proves DropExpired drops a partition whose newest
// row is past the cutoff and keeps one with recent data — judged by data, not by name.
func TestPgPartition_DropExpiredByData(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_drop"
	q := pq.QuoteIdentifier(tbl)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+q)
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+q) })

	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	nextMonth := thisMonth.AddDate(0, 1, 0)
	oldStart := thisMonth.AddDate(0, -6, 0)
	oldEnd := oldStart.AddDate(0, 1, 0)

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s (id BIGSERIAL, created_at TIMESTAMPTZ NOT NULL) PARTITION BY RANGE (created_at)", q))
	require.NoError(t, err)
	oldName := tbl + "_old"
	curName := tbl + "_cur"
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(oldName), q, pq.QuoteLiteral(oldStart.Format("2006-01-02")), pq.QuoteLiteral(oldEnd.Format("2006-01-02"))))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(curName), q, pq.QuoteLiteral(thisMonth.Format("2006-01-02")), pq.QuoteLiteral(nextMonth.Format("2006-01-02"))))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(created_at) VALUES ($1)", q), oldStart.AddDate(0, 0, 5))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(created_at) VALUES (now())", q))
	require.NoError(t, err)

	// cutoff = now - 90d: the old partition's data is fully past it -> drop; current kept.
	cutoff := now.AddDate(0, 0, -90)
	_, err = pgpartition.DropExpired(ctx, integrationDB, tbl, "created_at", cutoff)
	require.NoError(t, err)

	var hasOld, hasCur bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1)`, oldName).Scan(&hasOld))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1)`, curName).Scan(&hasCur))
	require.False(t, hasOld, "expired partition (all data < cutoff) must be dropped")
	require.True(t, hasCur, "partition with recent data must be kept")
}

// TestPgPartition_OpsErrorLogsConvertedByMigration proves tk_037 (WAVE 2) converts
// ops_error_logs in the REAL migration sequence — applying its dynamic index capture/replay
// against the real ~18-index schema (incl trigram + partial), reassigning the id sequence
// to the parent (R-001), and adding the id index that the UpdateErrorResolution WHERE id=$1
// path needs after the PK is dropped.
func TestPgPartition_OpsErrorLogsConvertedByMigration(t *testing.T) {
	ctx := context.Background()
	ok, err := pgpartition.IsPartitioned(ctx, integrationDB, "ops_error_logs")
	require.NoError(t, err)
	require.True(t, ok, "tk_037 must convert ops_error_logs to a partitioned table")

	var seqOwner string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT t.relname FROM pg_depend d
		JOIN pg_class s ON s.oid = d.objid AND s.relname = 'ops_error_logs_id_seq'
		JOIN pg_class t ON t.oid = d.refobjid
		WHERE d.deptype = 'a'`).Scan(&seqOwner))
	require.Equal(t, "ops_error_logs", seqOwner, "id sequence must be owned by the parent")

	var hasIDIndex bool
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE tablename='ops_error_logs' AND indexname='idx_ops_error_logs_id')`).Scan(&hasIDIndex))
	require.True(t, hasIDIndex, "the id index for UpdateErrorResolution (WHERE id=$1) must exist")

	// The resolution UPDATE (non-key columns, WHERE id) must work post-conversion.
	id := int64(0)
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`INSERT INTO ops_error_logs(created_at, error_phase, error_type) VALUES (now(), 'test', 'test') RETURNING id`).Scan(&id))
	res, err := integrationDB.ExecContext(ctx,
		`UPDATE ops_error_logs SET resolved=true, resolved_at=now(), resolved_by_user_id=1 WHERE id=$1`, id)
	require.NoError(t, err)
	n, _ := res.RowsAffected()
	require.Equal(t, int64(1), n, "UPDATE ... WHERE id must affect exactly the one row")
	_, _ = integrationDB.ExecContext(ctx, `DELETE FROM ops_error_logs WHERE id=$1`, id)
}
