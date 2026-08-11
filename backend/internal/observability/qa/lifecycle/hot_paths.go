package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
)

const (
	blobRootName = "qa_blobs"
	dlqRootName  = "qa_dlq"
)

// HourBlobDir returns qa_blobs/YYYY/MM/DD/HH under dataDir.
func HourBlobDir(dataDir string, hourStart time.Time) (string, error) {
	return hourDir(dataDir, blobRootName, hourStart)
}

// HourDLQDir returns qa_dlq/YYYY/MM/DD/HH under dataDir.
func HourDLQDir(dataDir string, hourStart time.Time) (string, error) {
	return hourDir(dataDir, dlqRootName, hourStart)
}

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

// HourlyBlobKey returns the relative blob-store key for one UTC hour.
func HourlyBlobKey(hourStart time.Time, requestID string) string {
	return trajectory.HourlyBlobKey(hourStart, requestID)
}

// HourlyDLQKey returns qa_dlq/YYYY/MM/DD/HH/<request-id>.json.zst relative key.
func HourlyDLQKey(hourStart time.Time, requestID string) string {
	h := hourStart.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%02d/%02d/%s.json.zst",
		dlqRootName, h.Year(), int(h.Month()), h.Day(), h.Hour(), strings.TrimSpace(requestID))
}
