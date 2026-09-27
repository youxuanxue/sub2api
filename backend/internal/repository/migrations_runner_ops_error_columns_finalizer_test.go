package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// tk_101 is the finalizer that physically drops the nine writer-less ops_error_logs
// columns. Phase 1 (tk_100) was deliberately recorded WITHOUT executing, because its DROP
// removed columns the previously-deployed color's SQL still named. That deferral is over:
// the whole fleet runs a release that neither reads nor writes those columns, so tk_101
// must actually EXECUTE.
//
// Adding tk_101 to shouldRecordMigrationWithoutExecution would be compile-clean and
// silently leave the columns in place forever — the column-writer gate would keep passing
// (it reads the DDL, not the database), so nothing else would catch it. Pin the inverse.
// See docs/approved/ops-error-logs-column-contract.md.
func TestShouldRecordMigrationWithoutExecution_ExecutesOpsErrorColumnFinalizer(t *testing.T) {
	ctx := context.Background()

	skip, err := shouldRecordMigrationWithoutExecution(
		ctx, nil, "tk_101_ops_error_logs_finalize_unwritten_columns.sql",
	)
	require.NoError(t, err)
	require.False(t, skip,
		"tk_101 must execute: it is the only migration that physically drops the nine "+
			"writer-less columns, and tk_100 can never run again (already recorded, "+
			"checksum immutable)")

	// tk_100 is recorded on every existing database, so its checksum is frozen and it can
	// never execute again. The record-only branch that used to guard it is therefore gone;
	// on a fresh database it now runs against an empty table, which is harmless.
	skip, err = shouldRecordMigrationWithoutExecution(
		ctx, nil, "tk_100_ops_error_logs_drop_unwritten_columns.sql",
	)
	require.NoError(t, err)
	require.False(t, skip, "tk_100 no longer needs a record-only exception")

	// Neighbouring ops migrations must still execute.
	skip, err = shouldRecordMigrationWithoutExecution(ctx, nil, "tk_099_reasoning_pricing_rolling_compat.sql")
	require.NoError(t, err)
	require.False(t, skip, "unrelated migrations must still execute")
}
