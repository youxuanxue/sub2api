package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

// tk_100 declares a DROP COLUMN over the live ops_error_logs partition tree. It must be
// recorded WITHOUT executing: the DDL takes 93 ACCESS EXCLUSIVE locks and removes columns
// the previously-deployed color's SQL still names, so executing it breaks both the
// blue/green window (migrations run on new-color startup against the shared DB) and an
// image rollback. Dropping the branch is compile-clean and only fails in prod, on deploy.
// See docs/approved/ops-error-logs-column-contract.md.
func TestShouldRecordMigrationWithoutExecution_DefersRetainedOpsErrorColumnDrop(t *testing.T) {
	ctx := context.Background()

	skip, err := shouldRecordMigrationWithoutExecution(ctx, nil, migrations.RetainedOpsErrorColumnsMigration)
	require.NoError(t, err)
	require.True(t, skip,
		"tk_100's DROP COLUMN must not run while the old color still reads those columns")

	// The name must resolve to a migration that actually exists, or the branch guards
	// nothing and the DDL ships on the next deploy under a renamed file.
	_, statErr := migrations.FS.Open(migrations.RetainedOpsErrorColumnsMigration)
	require.NoError(t, statErr, "RetainedOpsErrorColumnsMigration must name a real migration file")

	// Neighbouring ops migrations must still execute.
	skip, err = shouldRecordMigrationWithoutExecution(ctx, nil, "tk_099_reasoning_pricing_rolling_compat.sql")
	require.NoError(t, err)
	require.False(t, skip, "unrelated migrations must still execute")
}
