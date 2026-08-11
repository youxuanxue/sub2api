//go:build unit

package pgpartition

import (
	"testing"
	"time"
)

func TestHourlyPartitionNameUTC(t *testing.T) {
	start := time.Date(2026, 1, 31, 23, 45, 0, 0, time.UTC)
	got := HourlyPartitionName("qa_records", start)
	want := "qa_records_20260131_23"
	if got != want {
		t.Fatalf("HourlyPartitionName()=%q want %q", got, want)
	}
}

func TestRetentionBoundaryUsesDatabaseHourSemantics(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)
	got := RetentionBoundary(now)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("RetentionBoundary()=%s want %s", got, want)
	}
}

func TestHourlyTargetRangesCrossMonthYear(t *testing.T) {
	now := time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC)
	ranges := HourlyTargetRanges(now, 2)
	if len(ranges) != 2 {
		t.Fatalf("len(ranges)=%d want 2", len(ranges))
	}
	if !ranges[0].Start.Equal(now) || !ranges[0].End.Equal(now.Add(time.Hour)) {
		t.Fatalf("first range=%+v", ranges[0])
	}
	if !ranges[1].Start.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("second start=%s", ranges[1].Start)
	}
}

func TestHourStartUTCNormalizesLocalInput(t *testing.T) {
	local := time.Date(2026, 8, 11, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	got := HourStartUTC(local)
	want := time.Date(2026, 8, 11, 7, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("HourStartUTC()=%s want %s", got, want)
	}
}

func TestCanonicalHourlyNameRejectsShiftedBound(t *testing.T) {
	lower := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	if isCanonicalHourlyName("qa_records_20260811_07", lower) {
		t.Fatal("shifted one-hour bound must not be canonical")
	}
}
