package trajectory

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWriteBlobFileConfinesPathsAndRefusesReplacement(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "blobs")
	path, err := WriteBlobFile(root, "2026/09/one.json.zst", strings.NewReader("first"))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	_, err = WriteBlobFile(root, "2026/09/one.json.zst", strings.NewReader("second"))
	require.ErrorIs(t, err, os.ErrExist)
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "first", string(body))

	require.NoError(t, os.Symlink(outer, filepath.Join(root, "outside")))
	for _, key := range []string{"../escaped", filepath.Join(outer, "absolute"), "outside/escaped"} {
		_, err := WriteBlobFile(root, key, strings.NewReader("must not escape"))
		require.Error(t, err, key)
	}
	_, err = os.Stat(filepath.Join(outer, "escaped"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

type failingBlobReader struct{}

func (failingBlobReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestWriteBlobFileCleansOnlyItsIncompleteWrite(t *testing.T) {
	root := t.TempDir()
	_, err := WriteBlobFile(root, "partial", failingBlobReader{})
	require.ErrorContains(t, err, "read failed")
	_, err = WriteBlobFile(root, "partial", bytes.NewBufferString("complete"))
	require.NoError(t, err)
}

func TestBlobKeysNeverUseCallerPathSegments(t *testing.T) {
	for _, id := range []string{"../../../../escape", "/absolute", "a/b", "a\\b", "\x00", ".."} {
		for _, key := range []string{BlobKey(2026, 9, 11, id), HourlyBlobKey(time.Now(), id)} {
			require.True(t, filepath.IsLocal(key), key)
			require.NotContains(t, key, "..")
		}
	}
}
