//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/partitionmaintenance"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/stretchr/testify/require"
)

// TestPartitionMaintenance_CoverageAcceptsAttachedLegacyPartition pins the coverage
// proof against the topology the conversion migrations actually leave behind, which
// is also the shape running in prod: tk_035 / tk_037 ATTACH history as
// `FOR VALUES FROM (MINVALUE) TO (next_month)`, so the CURRENT month is served by
// that one partition and the matching CREATE is skipped as a benign 42P17 overlap.
//
// A coverage query that only accepts two-sided literal bounds silently drops the
// legacy partition from the covered union and then reports the current month — a
// range that is provably writable — as uncovered. On the daily cron path that
// aborts partition maintenance before cleanup and writes an error heartbeat every
// night; unit tests with mocked bound strings cannot catch it because the defect
// lives in what PostgreSQL returns for pg_get_expr.
func TestPartitionMaintenance_CoverageAcceptsAttachedLegacyPartition(t *testing.T) {
	ctx := context.Background()

	for _, table := range []string{"ops_system_logs", "ops_error_logs"} {
		partitioned, err := pgpartition.IsPartitioned(ctx, integrationDB, table)
		require.NoError(t, err)
		require.True(t, partitioned, "%s must be converted by the migration sequence", table)

		// Prove the precondition this regression is about: history lives in an
		// attached partition whose lower bound is MINVALUE, not a literal.
		var unbounded bool
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM pg_inherits i
				JOIN pg_class c ON c.oid = i.inhrelid
				WHERE i.inhparent = to_regclass($1)
				  AND pg_get_expr(c.relpartbound, c.oid, true) LIKE 'FOR VALUES FROM (MINVALUE)%'
			)`, table).Scan(&unbounded))
		require.True(t, unbounded,
			"%s should keep its history in an attached MINVALUE legacy partition", table)
	}

	// The cron path: usage_logs is not converted by the migration sequence, so the
	// compatibility mode skips it and the two converted ops tables must verify.
	result, err := partitionmaintenance.Ensure(
		ctx,
		integrationDB,
		time.Now().UTC(),
		partitionmaintenance.ModeAllowUnpartitioned,
		partitionmaintenance.Options{},
	)
	require.NoError(t, err,
		"maintenance must accept a MINVALUE legacy partition as covering the current range")

	verified := make(map[string]int, len(result.Tables))
	for _, table := range result.Tables {
		verified[table.Table] = table.RangeCount
	}
	for _, table := range []string{"ops_system_logs", "ops_error_logs"} {
		require.Equal(t, 8, verified[table],
			"%s must prove today plus seven future UTC days are covered", table)
	}
}
