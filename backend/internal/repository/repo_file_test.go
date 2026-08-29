package repository

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFindFromWorkingDir_LocatesOpsSQLWithoutCallerPath(t *testing.T) {
	// Boundary: -trimpath makes runtime.Caller return a module path, not a
	// filesystem path. Repo-owned files must be found from the working directory.
	trimmedCaller := "github.com/Wei-Shaw/sub2api/internal/repository/partitionmaintenance_coverage_integration_test.go"
	require.NoFileExists(t, filepath.Join(filepath.Dir(trimmedCaller), "../../../ops/observability/data-layer-partition-coverage.sql"))

	path, err := findFromWorkingDir("ops/observability/data-layer-partition-coverage.sql")
	require.NoError(t, err)
	st, err := os.Stat(path)
	require.NoError(t, err)
	require.False(t, st.IsDir())
	require.Greater(t, st.Size(), int64(0))
}
