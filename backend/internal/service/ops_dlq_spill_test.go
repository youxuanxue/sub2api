package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsDLQSpillAllowWriteRateLimit(t *testing.T) {
	t.Setenv("OPS_DLQ_MAX_WRITES_PER_MIN", "2")
	resetOpsDLQSpillForTest()
	t.Cleanup(resetOpsDLQSpillForTest)

	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	require.True(t, opsDLQSpillAllowWrite(now))
	require.True(t, opsDLQSpillAllowWrite(now.Add(10*time.Second)))
	require.False(t, opsDLQSpillAllowWrite(now.Add(20*time.Second)))
	require.True(t, opsDLQSpillAllowWrite(now.Add(1*time.Minute)))
}

func TestPruneOpsDLQDirMaxFilesAndAge(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	policy := opsDLQSpillLimits{
		maxFiles:     2,
		maxBytes:     1 << 30,
		maxAge:       time.Hour,
		writesPerMin: 1000,
	}

	writeFile := func(name string, at time.Time) {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(path, at, at))
	}

	writeFile("old.json.zst", now.Add(-2*time.Hour))
	writeFile("a.json.zst", now.Add(-30*time.Minute))
	writeFile("b.json.zst", now.Add(-20*time.Minute))
	writeFile("c.json.zst", now.Add(-10*time.Minute))

	removed, err := pruneOpsDLQDir(dir, now, policy)
	require.NoError(t, err)
	require.GreaterOrEqual(t, removed, 2)

	names := listDLQNames(t, dir)
	require.Len(t, names, 2)
	require.Contains(t, names, "b.json.zst")
	require.Contains(t, names, "c.json.zst")
}

func TestPruneOpsDLQDirConvergesToMaxFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	policy := opsDLQSpillLimits{
		maxFiles:     2,
		maxBytes:     1 << 30,
		maxAge:       24 * time.Hour,
		writesPerMin: 1000,
	}
	for i := 0; i < 10; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%02d.json.zst", i))
		require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
		require.NoError(t, os.Chtimes(path, now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i)*time.Minute)))
	}

	removed, err := pruneOpsDLQDir(dir, now.Add(time.Hour), policy)
	require.NoError(t, err)
	require.Equal(t, 8, removed)
	require.Len(t, listDLQNames(t, dir), 2)
}

func TestPruneOpsDLQDirConvergesToMaxBytes(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	policy := opsDLQSpillLimits{
		maxFiles:     100,
		maxBytes:     5,
		maxAge:       24 * time.Hour,
		writesPerMin: 1000,
	}
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%02d.json.zst", i))
		require.NoError(t, os.WriteFile(path, []byte("abc"), 0o644))
		require.NoError(t, os.Chtimes(path, now.Add(time.Duration(i)*time.Minute), now.Add(time.Duration(i)*time.Minute)))
	}

	removed, err := pruneOpsDLQDir(dir, now.Add(time.Hour), policy)
	require.NoError(t, err)
	require.Equal(t, 2, removed)
	require.Equal(t, []string{"02.json.zst"}, listDLQNames(t, dir))
}

func TestPruneOpsDLQDirReportsRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, "old.json.zst")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	require.NoError(t, os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)))
	removeErr := fmt.Errorf("injected remove failure")
	_, err := pruneOpsDLQDirWithRemove(dir, now, opsDLQSpillLimits{
		maxFiles:     2,
		maxBytes:     1 << 30,
		maxAge:       time.Hour,
		writesPerMin: 1000,
	}, func(string) error { return removeErr })
	require.ErrorIs(t, err, removeErr)
	require.FileExists(t, path)
}

func TestOpsDLQFallbackContainsUnsafeRequestIDWithoutPathEscape(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	resetOpsDLQSpillForTest()
	t.Cleanup(resetOpsDLQSpillForTest)

	svc := &OpsService{}
	entry := &OpsInsertErrorLogInput{
		RequestID:  "../../escaped",
		ErrorPhase: "upstream",
		ErrorType:  "upstream_error",
		CreatedAt:  time.Now().UTC(),
	}
	require.NoError(t, svc.persistPreparedErrorFallback(context.Background(), entry, "test"))
	require.NoFileExists(t, filepath.Join(dataDir, "..", "escaped.json.zst"))
	files, err := filepath.Glob(filepath.Join(dataDir, "ops_dlq", "*.json.zst"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	payload := readOpsFallbackPayload(t, files[0])
	persisted, ok := payload["entry"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "../../escaped", persisted["RequestID"])
}

func TestPrepareOpsDLQSpillDropsWhenRateLimited(t *testing.T) {
	t.Setenv("OPS_DLQ_MAX_WRITES_PER_MIN", "1")
	resetOpsDLQSpillForTest()
	t.Cleanup(resetOpsDLQSpillForTest)

	dir := t.TempDir()
	now := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	ok, err := prepareOpsDLQSpill(dir, now)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = prepareOpsDLQSpill(dir, now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, ok)
}

func listDLQNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := make([]string, 0, len(ents))
	for _, ent := range ents {
		if !ent.IsDir() {
			out = append(out, ent.Name())
		}
	}
	return out
}
