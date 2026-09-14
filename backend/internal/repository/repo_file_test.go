package repository

import (
	"fmt"
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

func findFromWorkingDir(rel string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for {
		candidate := filepath.Join(dir, rel)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%s not found from %s", rel, wd)
		}
		dir = parent
	}
}
