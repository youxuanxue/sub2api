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

func TestCreatePartitionedIndexConcurrently_EffectiveModelExpressions(t *testing.T) {
	ctx := context.Background()
	table := "pgpart_itest_effective_models"
	qTable := pq.QuoteIdentifier(table)
	_, _ = integrationDB.ExecContext(ctx, "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+qTable+" CASCADE")
	})

	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id BIGSERIAL,
			created_at TIMESTAMPTZ NOT NULL,
			model TEXT NOT NULL,
			requested_model TEXT,
			upstream_model TEXT
		) PARTITION BY RANGE (created_at);
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-01-01') TO ('2026-02-01');
		CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('2026-02-01') TO ('2026-03-01');
		INSERT INTO %s (created_at, model, requested_model, upstream_model) VALUES
			('2026-01-15', 'fallback-a', 'requested-a', 'upstream-a'),
			('2026-02-15', 'fallback-b', '', NULL);`,
		qTable,
		pq.QuoteIdentifier(table+"_p1"), qTable,
		pq.QuoteIdentifier(table+"_p2"), qTable,
		qTable,
	))
	require.NoError(t, err)

	policies := append([]nonTransactionalIndexPolicy(nil), nonTransactionalIndexPolicies[usageLogsEffectiveModelIndexesMigration]...)
	require.Len(t, policies, 2)
	parentIndexes := []string{
		"idx_pgpart_itest_effective_requested",
		"idx_pgpart_itest_effective_upstream",
	}
	for i := range policies {
		policies[i].partitionedTable = table
		policies[i].indexName = parentIndexes[i]
		require.NoError(t, createPartitionedIndexConcurrently(ctx, integrationDB, policies[i]))
		require.NoError(t, createPartitionedIndexConcurrently(ctx, integrationDB, policies[i]))
	}

	var tablePartitions int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class parent ON parent.oid = i.inhparent
		WHERE parent.relname = $1`, table).Scan(&tablePartitions))
	require.Equal(t, 2, tablePartitions)

	for _, parentIndex := range parentIndexes {
		var valid bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT i.indisvalid
			FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = $1`, parentIndex).Scan(&valid))
		require.True(t, valid)

		var indexPartitions int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_inherits i
			JOIN pg_class parent ON parent.oid = i.inhparent
			WHERE parent.relname = $1`, parentIndex).Scan(&indexPartitions))
		require.Equal(t, tablePartitions, indexPartitions)
	}
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

func setupHourlyIntegrationTable(ctx context.Context, t *testing.T, tbl string) time.Time {
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
	return time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
}

func TestPgPartition_EnsureHourlyCoversFutureHorizon(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_hourly_cover"
	anchor := setupHourlyIntegrationTable(ctx, t, tbl)
	require.NoError(t, pgpartition.EnsureHourly(ctx, integrationDB, tbl, anchor, pgpartition.QARecordsHourlyHorizon))
	ranges := pgpartition.HourlyTargetRanges(anchor, pgpartition.QARecordsHourlyHorizon)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, integrationDB, tbl, ranges)
	require.NoError(t, err)
	require.Equal(t, len(ranges), covered)
}

func TestPgPartition_HourlyCoverageRejectsNoncanonicalChildName(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_hourly_noncanonical"
	anchor := setupHourlyIntegrationTable(ctx, t, tbl)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`,
		pq.QuoteIdentifier(tbl+"_wrong"),
		pq.QuoteIdentifier(tbl),
		pq.QuoteLiteral(anchor.Format(time.RFC3339)),
		pq.QuoteLiteral(anchor.Add(time.Hour).Format(time.RFC3339)),
	))
	require.NoError(t, err)

	ranges := pgpartition.HourlyTargetRanges(anchor, 1)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, integrationDB, tbl, ranges)
	require.NoError(t, err)
	require.Zero(t, covered, "a wrong child name must not satisfy canonical hourly coverage")
}

func TestPgPartition_HourlyWriteRoutesToChildPartition(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_hourly_write"
	anchor := setupHourlyIntegrationTable(ctx, t, tbl)
	require.NoError(t, pgpartition.EnsureHourly(ctx, integrationDB, tbl, anchor, 1))
	partitionName := pgpartition.HourlyPartitionName(tbl, anchor)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (created_at, request_id, payload) VALUES ($1, $2, 'row')`,
		pq.QuoteIdentifier(tbl),
	), anchor.Add(15*time.Minute), "req-hourly")
	require.NoError(t, err)
	var inChild int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM "+pq.QuoteIdentifier(partitionName)).Scan(&inChild))
	require.Equal(t, 1, inChild)
}

func TestPgPartition_DropExpiredHourlyUsesCatalogUpperBound(t *testing.T) {
	ctx := context.Background()
	tbl := "pgpart_itest_hourly_drop"
	anchor := setupHourlyIntegrationTable(ctx, t, tbl)
	expiredHour := anchor.Add(-25 * time.Hour)
	require.NoError(t, pgpartition.EnsureHourly(ctx, integrationDB, tbl, expiredHour, 1))
	partitionName := pgpartition.HourlyPartitionName(tbl, expiredHour)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (created_at, request_id, payload) VALUES ($1, 'old-req', 'old')`,
		pq.QuoteIdentifier(tbl),
	), expiredHour.Add(10*time.Minute))
	require.NoError(t, err)
	cutoff := pgpartition.RetentionBoundary(anchor)
	reclaimed, err := pgpartition.DropExpired(ctx, integrationDB, tbl, cutoff)
	require.NoError(t, err)
	require.GreaterOrEqual(t, reclaimed, int64(0))
	var exists bool
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, partitionName).Scan(&exists))
	require.False(t, exists)
}
