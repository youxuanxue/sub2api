//go:build unit

package archive

import (
	"testing"
	"time"
)

func TestShardPrefix(t *testing.T) {
	start := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	if got := ShardPrefix(start); got != "raw/v1/date=2026-08-06/hour=09" {
		t.Fatalf("ShardPrefix()=%q", got)
	}
}

func TestPreviousSealedHour(t *testing.T) {
	runAt := time.Date(2026, 8, 6, 10, 15, 0, 0, time.UTC)
	start, end := PreviousSealedHour(runAt)
	if !start.Equal(time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("start=%s", start)
	}
	if !end.Equal(time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("end=%s", end)
	}
}
