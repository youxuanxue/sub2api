//go:build unit

package repository

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestOpsInsertErrorLogSQLColumnsArgsAndPlaceholdersAgree derives all three counts
// from insertOpsErrorLogSQL instead of hardcoding one. A literal ("must be 38 args")
// only fails *after* someone has already mismatched the column list and the arg
// slice; deriving them means adding a column without its arg fails here immediately.
func TestOpsInsertErrorLogSQLColumnsArgsAndPlaceholdersAgree(t *testing.T) {
	open := strings.Index(insertOpsErrorLogSQL, "(")
	close := strings.Index(insertOpsErrorLogSQL, ")")
	require.Greater(t, close, open, "cannot locate the INSERT column list")

	columns := []string{}
	for _, raw := range strings.Split(insertOpsErrorLogSQL[open+1:close], ",") {
		if name := strings.TrimSpace(raw); name != "" {
			columns = append(columns, name)
		}
	}
	require.NotEmpty(t, columns)

	placeholders := regexp.MustCompile(`\$\d+`).FindAllString(insertOpsErrorLogSQL, -1)
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{})

	require.Len(t, placeholders, len(columns),
		"INSERT placeholder count must match the column list")
	require.Len(t, args, len(columns),
		"opsInsertErrorLogArgs must supply exactly one value per INSERT column")

	// The deleted-key attribution columns must stay in the INSERT: they are read by
	// ops_repo_user_visible_failure_tk.go (the SLA numerator) and by the billing-watch
	// probe, and dropping them silently un-attributes failures on deleted keys.
	for _, required := range []string{
		"attempted_key_prefix", "deleted_key_owner_user_id", "deleted_key_name",
	} {
		require.Contains(t, columns, required)
	}
}

func TestOpsInsertErrorLogArgsPreservesExplicitZeroUpstreamStatus(t *testing.T) {
	zero := 0
	input := &service.OpsInsertErrorLogInput{UpstreamStatusCode: &zero}
	args := opsInsertErrorLogArgs(input)

	// Locate upstream_status_code by name rather than by a magic index, so adding a
	// column earlier in the list cannot silently point this assertion at a neighbour.
	idx := opsInsertColumnIndex(t, "upstream_status_code")
	encoded, ok := args[idx].(sql.NullInt64)
	require.True(t, ok)
	require.True(t, encoded.Valid)
	require.Zero(t, encoded.Int64)
}

// opsInsertColumnIndex returns the 0-based position of a column in the INSERT list.
func opsInsertColumnIndex(t *testing.T, column string) int {
	t.Helper()
	open := strings.Index(insertOpsErrorLogSQL, "(")
	close := strings.Index(insertOpsErrorLogSQL, ")")
	require.Greater(t, close, open)
	for i, raw := range strings.Split(insertOpsErrorLogSQL[open+1:close], ",") {
		if strings.TrimSpace(raw) == column {
			return i
		}
	}
	t.Fatalf("column %q not found in insertOpsErrorLogSQL", column)
	return -1
}

func TestOpsNullableIntPointerDistinguishesNilZeroAndStatus(t *testing.T) {
	missing := opsNullableIntPointer(nil).(sql.NullInt64)
	require.False(t, missing.Valid)

	zeroValue := 0
	zero := opsNullableIntPointer(&zeroValue).(sql.NullInt64)
	require.True(t, zero.Valid)
	require.Zero(t, zero.Int64)

	statusValue := 503
	status := opsNullableIntPointer(&statusValue).(sql.NullInt64)
	require.True(t, status.Valid)
	require.EqualValues(t, 503, status.Int64)
}
