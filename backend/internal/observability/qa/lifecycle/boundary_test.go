//go:build unit

package lifecycle

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

func TestRetentionUntilForHourUsesNonAuthoritativeSourceHourUpperBound(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	got := RetentionUntilForHour(hour)
	want := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("RetentionUntilForHour()=%s want %s", got, want)
	}
}

func TestUsesHourlyStorageRespectsCutoverHour(t *testing.T) {
	cutover := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	before := cutover.Add(-time.Minute)
	after := cutover
	if UsesHourlyStorage(cutover, before) {
		t.Fatal("expected pre-cutover write to use legacy layout")
	}
	if !UsesHourlyStorage(cutover, after) {
		t.Fatal("expected post-cutover write to use hourly layout")
	}
}

func TestParseHourlyCutoverUTCStrict(t *testing.T) {
	got, err := ParseHourlyCutoverUTCStrict("2026-08-11T12:00:00Z")
	if err != nil {
		t.Fatalf("ParseHourlyCutoverUTCStrict() err=%v", err)
	}
	want := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ParseHourlyCutoverUTCStrict()=%s want %s", got, want)
	}
	if _, err := ParseHourlyCutoverUTCStrict("not-a-time"); err == nil {
		t.Fatal("invalid cutover must fail")
	}
	if _, err := ParseHourlyCutoverUTCStrict("2026-08-11T12:30:00Z"); err == nil {
		t.Fatal("non-hour cutover must fail")
	}
}

func TestHourlyTargetRangeCountIsExclusiveHorizon(t *testing.T) {
	ranges := pgpartition.HourlyTargetRanges(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC), HourlyHorizon)
	if len(ranges) != HourlyHorizon {
		t.Fatalf("HourlyTargetRanges() len=%d want %d", len(ranges), HourlyHorizon)
	}
}
