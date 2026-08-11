//go:build unit

package lifecycle

import (
	"testing"
	"time"
)

func TestHourlyBlobKeyLayout(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	got := HourlyBlobKey(hour, "req-abc")
	want := "2026/08/11/07/re/req-abc.json.zst"
	if got != want {
		t.Fatalf("HourlyBlobKey()=%q want %q", got, want)
	}
}

func TestValidateHourDirRejectsEscape(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if err := ValidateHourDir("/app/data", "qa_blobs", hour, "/app/data/qa_blobs/2026/08/11/08"); err == nil {
		t.Fatal("ValidateHourDir() must reject adjacent hour directory")
	}
}

func TestRetentionUntilForHourUsesUpperBoundPlus24h(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	got := RetentionUntilForHour(hour)
	want := hour.Add(25 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("RetentionUntilForHour()=%s want %s", got, want)
	}
}
