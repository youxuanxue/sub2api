//go:build unit

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInspectLegacyHotFilesCountsOnlyNonHourlyLayouts(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		"qa_blobs/2026/08/11/12/re/hourly.json.zst",
		"qa_blobs/2026/08/11/re/legacy.json.zst",
		"qa_dlq/2026/08/11/12/hourly.json.zst",
		"qa_dlq/legacy.json.zst",
	}
	for _, rel := range paths {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := InspectLegacyHotFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.BlobFiles != 1 || got.DLQFiles != 1 {
		t.Fatalf("InspectLegacyHotFiles()=%+v", got)
	}
}

func TestValidateHourDirRejectsEscape(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if err := ValidateHourDir("/app/data", "qa_blobs", hour, "/app/data/qa_blobs/2026/08/11/08"); err == nil {
		t.Fatal("ValidateHourDir() must reject adjacent hour directory")
	}
}
