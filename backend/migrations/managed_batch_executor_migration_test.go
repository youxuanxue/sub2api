package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedBatchExecutorMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("tk_107_managed_batch_executor.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, table := range []string{"batch_files", "batches", "batch_items", "batch_shards", "batch_attempts", "batch_events", "batch_outbox"} {
		require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS "+table)
	}
	require.Contains(t, sql, "unknown_result BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "UNIQUE INDEX IF NOT EXISTS batch_outbox_dedup_uq")
}
