package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountsNameSortIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("tk_106_accounts_name_sort_notx.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_name_id_active")
	require.Contains(t, sql, "ON accounts (name, id)")
	require.Contains(t, sql, "WHERE deleted_at IS NULL")
}
