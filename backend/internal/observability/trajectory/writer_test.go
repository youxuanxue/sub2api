package trajectory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriterUsesSafeBoundedDLQBasename(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter(nil, dir)

	uri, err := writer.Write(context.Background(), "unused", []byte("payload"), "../../escaped")
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, "..", "escaped.json.zst"))
	files, err := filepath.Glob(filepath.Join(dir, "*.json.zst"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "payload", string(requireFile(t, files[0])))
	require.Equal(t, "dlq://"+files[0], uri)

	longURI, err := writer.Write(context.Background(), "unused", []byte("long"), strings.Repeat("a", 1024))
	require.NoError(t, err)
	require.LessOrEqual(t, len(filepath.Base(strings.TrimPrefix(longURI, "dlq://"))), len("dlq-")+32+len(".json.zst"))
}

func TestWriterPreservesSafeCorrelationIDBasename(t *testing.T) {
	dir := t.TempDir()
	writer := NewWriter(nil, dir)

	uri, err := writer.Write(context.Background(), "unused", []byte("payload"), "req-safe_123.abc")
	require.NoError(t, err)
	require.Equal(t, "dlq://"+filepath.Join(dir, "req-safe_123.abc.json.zst"), uri)
}

func requireFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	return body
}
