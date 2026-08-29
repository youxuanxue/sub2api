//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
type partitionProbeStats struct {
	OpsErrorCurrent  bool `json:"ops_error_logs_current_covered"`
	OpsErrorFuture   bool `json:"ops_error_logs_future_covered"`
	OpsSystemCurrent bool `json:"ops_system_logs_current_covered"`
	OpsSystemFuture  bool `json:"ops_system_logs_future_covered"`
	UsageCurrent     bool `json:"usage_logs_current_covered"`
	UsageFuture      bool `json:"usage_logs_future_covered"`
}

func TestDataLayerSafetyProbePartitionCoverage(t *testing.T) {
	queryPath, err := findFromWorkingDir("ops/observability/data-layer-partition-coverage.sql")
	require.NoError(t, err)
	query, err := os.ReadFile(queryPath)
	require.NoError(t, err)

	cases := []struct {
		name        string
		partitions  []string
		wantCurrent bool
		wantFuture  bool
	}{
		{name: "daily", partitions: []string{
			"FOR VALUES FROM (CURRENT_DATE) TO (CURRENT_DATE + 1)",
			"FOR VALUES FROM (CURRENT_DATE + 1) TO (CURRENT_DATE + 8)",
		}, wantCurrent: true, wantFuture: true},
		{name: "monthly", partitions: []string{
			"FOR VALUES FROM (date_trunc('month', CURRENT_DATE)) TO (date_trunc('month', CURRENT_DATE) + interval '2 months')",
		}, wantCurrent: true, wantFuture: true},
		{name: "mixed", partitions: []string{
			"FOR VALUES FROM (CURRENT_DATE) TO (CURRENT_DATE + 3)",
			"FOR VALUES FROM (CURRENT_DATE + 3) TO (CURRENT_DATE + 8)",
		}, wantCurrent: true, wantFuture: true},
		{name: "minvalue", partitions: []string{
			"FOR VALUES FROM (MINVALUE) TO (CURRENT_DATE + 8)",
		}, wantCurrent: true, wantFuture: true},
		{name: "gap", partitions: []string{
			"FOR VALUES FROM (CURRENT_DATE) TO (CURRENT_DATE + 3)",
			"FOR VALUES FROM (CURRENT_DATE + 4) TO (CURRENT_DATE + 8)",
		}, wantCurrent: true, wantFuture: false},
		{name: "default", partitions: []string{"DEFAULT"}, wantCurrent: false, wantFuture: false},
		{name: "maxvalue", partitions: []string{
			"FOR VALUES FROM (CURRENT_DATE) TO (MAXVALUE)",
		}, wantCurrent: false, wantFuture: false},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			schema := "probe_" + strings.ReplaceAll(test.name, "-", "_")
			_, err := integrationDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
			require.NoError(t, err)
			t.Cleanup(func() {
				_, _ = integrationDB.ExecContext(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
			})
			_, err = integrationDB.ExecContext(context.Background(), "CREATE SCHEMA "+schema)
			require.NoError(t, err)

			conn, err := integrationDB.Conn(context.Background())
			require.NoError(t, err)
			defer func() {
				_, _ = conn.ExecContext(context.Background(), "RESET search_path")
				_ = conn.Close()
			}()
			_, err = conn.ExecContext(context.Background(), "SET search_path TO "+schema)
			require.NoError(t, err)
			_, err = conn.ExecContext(context.Background(), `CREATE TABLE ops_job_heartbeats (
				job_name text PRIMARY KEY, last_success_at timestamptz, last_error_at timestamptz
			)`)
			require.NoError(t, err)
			for _, table := range []string{"ops_error_logs", "ops_system_logs", "usage_logs"} {
				_, err = conn.ExecContext(context.Background(), fmt.Sprintf(
					"CREATE TABLE %s (created_at timestamptz NOT NULL) PARTITION BY RANGE (created_at)", table))
				require.NoError(t, err)
				for index, bound := range test.partitions {
					_, err = conn.ExecContext(context.Background(), fmt.Sprintf(
						"CREATE TABLE %s_part_%d PARTITION OF %s %s", table, index, table, bound))
					require.NoError(t, err)
				}
			}

			var output string
			require.NoError(t, conn.QueryRowContext(context.Background(), string(query)).Scan(&output))
			var stats partitionProbeStats
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(output, "PARTITIONSTATS ")), &stats))
			for _, covered := range []bool{
				stats.OpsErrorCurrent, stats.OpsSystemCurrent, stats.UsageCurrent,
			} {
				require.Equal(t, test.wantCurrent, covered, "%s current coverage mismatch", test.name)
			}
			for _, covered := range []bool{
				stats.OpsErrorFuture, stats.OpsSystemFuture, stats.UsageFuture,
			} {
				require.Equal(t, test.wantFuture, covered, "%s future coverage mismatch", test.name)
			}
		})
	}
}

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
