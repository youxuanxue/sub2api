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

func TestCreatePartitionedIndexConcurrently_PartialIndexWhereClause(t *testing.T) {
	ctx := context.Background()
	table := "pgpart_itest_partial_online_index"
	parentIndex := "idx_pgpart_itest_partial_mismatch_created"
	qTable := pq.QuoteIdentifier(table)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	})

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id BIGSERIAL,
			created_at TIMESTAMPTZ NOT NULL,
			upstream_model_mismatch BOOLEAN
		) PARTITION BY RANGE (created_at);
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
		INSERT INTO %s VALUES (DEFAULT, '2026-01-15', true), (DEFAULT, '2026-02-15', false);`,
		qTable,
		pq.QuoteIdentifier(table+"_p1"), qTable,
		pq.QuoteIdentifier(table+"_p2"), qTable,
		qTable,
	))
	require.NoError(t, err)

	policy := nonTransactionalIndexPolicy{
		indexName:             parentIndex,
		partitionedTable:      table,
		partitionedIndexExpr:  "created_at DESC, id DESC",
		partitionedIndexWhere: "upstream_model_mismatch IS TRUE",
	}
	require.NoError(t, createPartitionedIndexConcurrently(ctx, integrationDB, policy))
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

// TestPgPartition_DropExpiredByBound proves retention uses declared bounds: an empty
// expired partition is dropped while an empty current/future partition is preserved.
func TestPgPartition_DropExpiredByBound(t *testing.T) {
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
	legacyName := tbl + "_legacy"
	curName := tbl + "_cur"
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(oldName), q, pq.QuoteLiteral(oldStart.Format("2006-01-02")), pq.QuoteLiteral(oldEnd.Format("2006-01-02"))))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(legacyName), q, pq.QuoteLiteral(oldEnd.Format("2006-01-02")), pq.QuoteLiteral(thisMonth.Format("2006-01-02"))))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(curName), q, pq.QuoteLiteral(thisMonth.Format("2006-01-02")), pq.QuoteLiteral(nextMonth.Format("2006-01-02"))))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(created_at) VALUES ($1)", q), oldEnd.AddDate(0, 0, 1))
	require.NoError(t, err)
	// The old and current partitions are empty. Their bounds alone decide retention;
	// the legacy partition has only expired data but a still-live upper bound.
	cutoff := now.AddDate(0, 0, -90)
	_, err = pgpartition.DropExpired(ctx, integrationDB, tbl, cutoff)
	require.NoError(t, err)

	var hasOld, hasLegacy, hasCur bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1)`, oldName).Scan(&hasOld))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1)`, legacyName).Scan(&hasLegacy))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class WHERE relname=$1)`, curName).Scan(&hasCur))
	require.False(t, hasOld, "empty partition with upper bound <= cutoff must be dropped")
	require.True(t, hasLegacy, "bound-straddling partition must remain even when every current row is expired")
	require.True(t, hasCur, "empty partition with upper bound > cutoff must be kept")

	straddling, err := pgpartition.ListStraddling(ctx, integrationDB, tbl, "created_at", cutoff)
	require.NoError(t, err)
	require.Equal(t, []string{legacyName}, straddling, "surviving legacy partition still needs row-level reclaim")
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

// TestPgPartition_RehomeDefaultMonthlyDrainsDefaultBeforeAttach proves PostgreSQL
// rejects CREATE TABLE ... PARTITION OF while DEFAULT still holds rows in the target
// range, and that staging-first rehome succeeds end-to-end.
func TestPgPartition_RehomeDefaultMonthlyDrainsDefaultBeforeAttach(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_rehome"
	q := pq.QuoteIdentifier(tbl)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+q+" CASCADE")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+q+" CASCADE")
	})

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (
			id BIGSERIAL,
			created_at TIMESTAMPTZ NOT NULL,
			request_id TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (id, created_at)
		) PARTITION BY RANGE (created_at)`, q))
	require.NoError(t, err)
	defaultName := tbl + "_default"
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s DEFAULT",
		pq.QuoteIdentifier(defaultName), q,
	))
	require.NoError(t, err)

	monthStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := monthStart.AddDate(0, 1, 0)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (created_at, request_id, payload) VALUES
		 ($1, 'req-a', 'a'),
		 ($2, 'req-b', 'b')`,
		q,
	), monthStart.Add(24*time.Hour), monthStart.Add(48*time.Hour))
	require.NoError(t, err)

	partitionName := pgpartition.MonthlyPartitionName(tbl, monthStart)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(partitionName), q,
		pq.QuoteLiteral(monthStart.Format(time.RFC3339)),
		pq.QuoteLiteral(monthEnd.Format(time.RFC3339)),
	))
	require.Error(t, err, "PostgreSQL must reject bounded partition create while DEFAULT holds rows in range")

	result, err := pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", monthStart.Add(15*24*time.Hour),
		pgpartition.RehomeOptions{
			BatchSize:     5000,
			MaxRowsPerRun: 20000,
			DedupColumns:  []string{"created_at", "request_id"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), result.RemainingRows)
	require.Len(t, result.Months, 1)
	require.Equal(t, int64(2), result.Months[0].RowsMoved)

	var attached bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_inherits i
		  JOIN pg_class child ON child.oid = i.inhrelid
		  JOIN pg_class parent ON parent.oid = i.inhparent
		 WHERE parent.relname = $1 AND child.relname = $2
		)`, tbl, partitionName).Scan(&attached))
	require.True(t, attached)

	var inPartition int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inPartition))
	require.Equal(t, 2, inPartition)
}

func setupRehomeIntegrationTable(ctx context.Context, t *testing.T, tbl string) time.Time {
	t.Helper()
	q := pq.QuoteIdentifier(tbl)
	_, err := integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+q+" CASCADE")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+q+" CASCADE")
	})
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s (
			id BIGSERIAL,
			created_at TIMESTAMPTZ NOT NULL,
			request_id TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (id, created_at)
		) PARTITION BY RANGE (created_at)`, q))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s DEFAULT",
		pq.QuoteIdentifier(tbl+"_default"), q,
	))
	require.NoError(t, err)
	return time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
}

func TestPgPartition_RehomeDefaultMultiTickBudget(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_rehome_multitick"
	monthStart := setupRehomeIntegrationTable(ctx, t, tbl)
	now := monthStart.Add(15 * 24 * time.Hour)
	opts := pgpartition.RehomeOptions{
		BatchSize:     5000,
		MaxRowsPerRun: 2,
		DedupColumns:  []string{"created_at", "request_id"},
	}
	for i := 0; i < 5; i++ {
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (created_at, request_id, payload) VALUES ($1, $2, $3)`,
			pq.QuoteIdentifier(tbl),
		), monthStart.Add(time.Duration(i+1)*24*time.Hour), fmt.Sprintf("req-%d", i), fmt.Sprintf("row-%d", i))
		require.NoError(t, err)
	}

	result, err := pgpartition.RehomeDefaultMonthly(ctx, integrationDB, tbl, "created_at", now, opts)
	require.NoError(t, err)
	require.True(t, result.BudgetExhausted)
	require.True(t, result.PendingFinalize)
	require.Equal(t, int64(2), result.RowsMoved)

	result, err = pgpartition.RehomeDefaultMonthly(ctx, integrationDB, tbl, "created_at", now, opts)
	require.NoError(t, err)
	require.True(t, result.BudgetExhausted)
	require.True(t, result.PendingFinalize)
	require.Equal(t, int64(2), result.RowsMoved)

	result, err = pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", now,
		pgpartition.RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 10, DedupColumns: opts.DedupColumns},
	)
	require.NoError(t, err)
	require.False(t, result.PendingFinalize)
	require.Equal(t, int64(0), result.RemainingRows)
	require.Equal(t, int64(5), result.RowsMoved)

	partitionName := pgpartition.MonthlyPartitionName(tbl, monthStart)
	var inPartition int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inPartition))
	require.Equal(t, 5, inPartition)
}

func TestPgPartition_RehomeDefaultCompositeIdentityDedup(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_rehome_dedup"
	monthStart := setupRehomeIntegrationTable(ctx, t, tbl)
	now := monthStart.Add(15 * 24 * time.Hour)
	sharedRequestID := "shared-request-id"
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (created_at, request_id, payload) VALUES
		 ($1, $2, 'first'),
		 ($3, $2, 'second')`,
		pq.QuoteIdentifier(tbl),
	), monthStart.Add(24*time.Hour), sharedRequestID, monthStart.Add(48*time.Hour))
	require.NoError(t, err)

	result, err := pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", now,
		pgpartition.RehomeOptions{
			BatchSize:     5000,
			MaxRowsPerRun: 20000,
			DedupColumns:  []string{"created_at", "request_id"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(0), result.RemainingRows)
	require.Equal(t, int64(2), result.RowsMoved)

	partitionName := pgpartition.MonthlyPartitionName(tbl, monthStart)
	var inPartition int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inPartition))
	require.Equal(t, 2, inPartition)
}

func TestPgPartition_RehomeAttachedOrphanStagingRecovery(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_rehome_orphan"
	monthStart := setupRehomeIntegrationTable(ctx, t, tbl)
	monthEnd := monthStart.AddDate(0, 1, 0)
	now := monthStart.Add(15 * 24 * time.Hour)
	partitionName := pgpartition.MonthlyPartitionName(tbl, monthStart)
	stagingName := partitionName + "_rehome_staging"
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)",
		pq.QuoteIdentifier(partitionName), pq.QuoteIdentifier(tbl),
		pq.QuoteLiteral(monthStart.Format(time.RFC3339)),
		pq.QuoteLiteral(monthEnd.Format(time.RFC3339)),
	))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		"CREATE TABLE %s (LIKE %s INCLUDING DEFAULTS INCLUDING GENERATED)",
		pq.QuoteIdentifier(stagingName), pq.QuoteIdentifier(tbl+"_default"),
	))
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (created_at, request_id, payload) VALUES
		 ($1, 'orph-1', 'one'),
		 ($2, 'orph-2', 'two')`,
		pq.QuoteIdentifier(stagingName),
	), monthStart.Add(24*time.Hour), monthStart.Add(48*time.Hour))
	require.NoError(t, err)

	result, err := pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", now,
		pgpartition.RehomeOptions{
			BatchSize:     5000,
			MaxRowsPerRun: 20000,
			DedupColumns:  []string{"created_at", "request_id"},
		},
	)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.RowsMoved)
	require.Equal(t, int64(0), result.RemainingRows)

	var inPartition int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inPartition))
	require.Equal(t, 2, inPartition)

	var stagingExists bool
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, stagingName).Scan(&stagingExists))
	require.False(t, stagingExists)
}

func TestPgPartition_RehomeFinalizeBlocksConcurrentCaptureEndToEnd(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_rehome_finalize_lock"
	monthStart := setupRehomeIntegrationTable(ctx, t, tbl)
	now := monthStart.Add(15 * 24 * time.Hour)
	q := pq.QuoteIdentifier(tbl)
	dedup := []string{"created_at", "request_id"}
	lateCreatedAt := monthStart.Add(72 * time.Hour)
	lateRequestID := "late-req"

	for i := 0; i < 2; i++ {
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (created_at, request_id, payload) VALUES ($1, $2, $3)`,
			q,
		), monthStart.Add(time.Duration(i+1)*24*time.Hour), fmt.Sprintf("req-%d", i), fmt.Sprintf("row-%d", i))
		require.NoError(t, err)
	}

	partial, err := pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", now,
		pgpartition.RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 2, DedupColumns: dedup},
	)
	require.NoError(t, err)
	require.True(t, partial.BudgetExhausted)
	require.True(t, partial.PendingFinalize)
	require.Equal(t, int64(2), partial.RowsMoved)

	rehomeDone := make(chan error, 1)
	insertDone := make(chan error, 1)
	lockObserved := make(chan bool, 1)

	go func() {
		_, err := pgpartition.RehomeDefaultMonthly(
			ctx, integrationDB, tbl, "created_at", now,
			pgpartition.RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 100, DedupColumns: dedup},
		)
		rehomeDone <- err
	}()

	go func() {
		observed := waitForTableShareRowExclusiveLock(ctx, tbl, 15*time.Second)
		lockObserved <- observed
		if !observed {
			insertDone <- fmt.Errorf("finalize parent lock was not observed")
			return
		}
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (created_at, request_id, payload) VALUES ($1, $2, 'late')`,
			q,
		), lateCreatedAt, lateRequestID)
		insertDone <- err
	}()

	require.True(t, <-lockObserved, "finalizeStagingPartition must acquire ShareRowExclusiveLock")
	require.NoError(t, <-rehomeDone)
	require.NoError(t, <-insertDone, "concurrent capture must succeed after finalize commits")

	followUp, err := pgpartition.RehomeDefaultMonthly(
		ctx, integrationDB, tbl, "created_at", now,
		pgpartition.RehomeOptions{BatchSize: 5000, MaxRowsPerRun: 100, DedupColumns: dedup},
	)
	require.NoError(t, err)
	require.Equal(t, int64(1), followUp.RowsMoved)
	require.Equal(t, int64(0), followUp.RemainingRows)

	partitionName := pgpartition.MonthlyPartitionName(tbl, monthStart)
	var inPartition int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inPartition))
	require.Equal(t, 3, inPartition)

	var distinctIdentity int
	require.NoError(t, integrationDB.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(DISTINCT (created_at, request_id)) FROM %s`,
		pq.QuoteIdentifier(partitionName),
	)).Scan(&distinctIdentity))
	require.Equal(t, 3, distinctIdentity)
}

func waitForTableShareRowExclusiveLock(ctx context.Context, table string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var locked bool
		err := integrationDB.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1
			    FROM pg_locks l
			    JOIN pg_class c ON c.oid = l.relation
			   WHERE c.relname = $1
			     AND l.mode = 'ShareRowExclusiveLock'
			)`, table).Scan(&locked)
		if err == nil && locked {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
