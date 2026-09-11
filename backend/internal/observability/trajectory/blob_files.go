package trajectory

import (
	"io"
	"os"
	"path/filepath"
)

// WriteBlobFile publishes a new private blob below root. os.Root confines
// traversal and symlink resolution; exclusive creation also protects retries
// and the DLQ from replacing evidence that has already been written.
func WriteBlobFile(root, key string, body io.Reader) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.FromSlash(key)
	if err := dir.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return "", err
	}
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = dir.Remove(name)
		if copyErr != nil {
			return "", copyErr
		}
		return "", closeErr
	}
	return filepath.Join(root, name), nil
}
