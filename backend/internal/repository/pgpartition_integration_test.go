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
	require.NoError(t, pgpartition.EnsureMonthly(ctx, integrationDB, tbl, "created_at", now, 2))

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
