package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	blobRootName = "qa_blobs"
	dlqRootName  = "qa_dlq"
)

func hourDir(dataDir, root string, hourStart time.Time) (string, error) {
	base, err := canonicalDataRoot(dataDir)
	if err != nil {
		return "", err
	}
	h := hourStart.UTC()
	rel := filepath.Join(
		root,
		fmt.Sprintf("%04d", h.Year()),
		fmt.Sprintf("%02d", int(h.Month())),
		fmt.Sprintf("%02d", h.Day()),
		fmt.Sprintf("%02d", h.Hour()),
	)
	return filepath.Join(base, rel), nil
}

func canonicalDataRoot(dataDir string) (string, error) {
	root := strings.TrimSpace(dataDir)
	if root == "" {
		root = "/app/data"
	}
	clean := filepath.Clean(root)
	if clean != root && clean+"/" != root {
		return "", fmt.Errorf("lifecycle: noncanonical data dir %q", dataDir)
	}
	return clean, nil
}

// ValidateHourDir ensures path is the exact hour directory under dataDir and rejects escapes.
func ValidateHourDir(dataDir, root string, hourStart time.Time, candidate string) error {
	want, err := hourDir(dataDir, root, hourStart)
	if err != nil {
		return err
	}
	if err := validateHourPathComponents(dataDir, root, hourStart); err != nil {
		return err
	}
	absWant, err := filepath.Abs(want)
	if err != nil {
		return err
	}
	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return err
	}
	if absCandidate != absWant {
		return fmt.Errorf("lifecycle: hour cleanup path %q is outside bound %q", absCandidate, absWant)
	}
	info, err := os.Lstat(absCandidate)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lifecycle: hour cleanup path %q is a symlink", absCandidate)
	}
	return nil
}

func validateHourPathComponents(dataDir, root string, hourStart time.Time) error {
	base, err := canonicalDataRoot(dataDir)
	if err != nil {
		return err
	}
	h := hourStart.UTC()
	parts := []string{
		root,
		fmt.Sprintf("%04d", h.Year()),
		fmt.Sprintf("%02d", int(h.Month())),
		fmt.Sprintf("%02d", h.Day()),
		fmt.Sprintf("%02d", h.Hour()),
	}
	current := base
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: hour cleanup ancestor %q is a symlink", current)
		}
	}
	return nil
}

// RemoveHourDirectory deletes an exact hour directory after validation.
func RemoveHourDirectory(dataDir, root string, hourStart time.Time) error {
	dir, err := hourDir(dataDir, root, hourStart)
	if err != nil {
		return err
	}
	if err := ValidateHourDir(dataDir, root, hourStart, dir); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("lifecycle: remove hour dir %s: %w", dir, err)
	}
	return nil
}

type LegacyHotFileInventory struct {
	BlobFiles int64
	DLQFiles  int64
}

// InspectLegacyHotFiles counts files that do not use the canonical UTC-hour layout.
func InspectLegacyHotFiles(dataDir string) (LegacyHotFileInventory, error) {
	base, err := canonicalDataRoot(dataDir)
	if err != nil {
		return LegacyHotFileInventory{}, err
	}
	blobs, err := countLegacyLayoutFiles(filepath.Join(base, blobRootName), 6)
	if err != nil {
		return LegacyHotFileInventory{}, err
	}
	dlq, err := countLegacyLayoutFiles(filepath.Join(base, dlqRootName), 5)
	if err != nil {
		return LegacyHotFileInventory{}, err
	}
	return LegacyHotFileInventory{BlobFiles: blobs, DLQFiles: dlq}, nil
}

func countLegacyLayoutFiles(root string, hourlyParts int) (int64, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return 0, fmt.Errorf("lifecycle: inspect hot root %s: %w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, fmt.Errorf("lifecycle: hot root %s is not a canonical directory", root)
	}
	var count int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: hot path %s is a symlink", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("lifecycle: hot path %s is not a regular file", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != hourlyParts || !validHourPathPrefix(parts) {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("lifecycle: inventory hot files under %s: %w", root, err)
	}
	return count, nil
}

func validHourPathPrefix(parts []string) bool {
	if len(parts) < 4 {
		return false
	}
	_, err := time.Parse("2006/01/02/15", strings.Join(parts[:4], "/"))
	return err == nil
}
