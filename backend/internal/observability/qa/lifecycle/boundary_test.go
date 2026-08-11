//go:build unit

package lifecycle

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

func TestRetentionUntilForHourUsesUpperBoundPlus25h(t *testing.T) {
	hour := time.Date(2026, 8, 11, 7, 30, 0, 0, time.UTC)
	got := RetentionUntilForHour(hour)
	want := time.Date(2026, 8, 12, 8, 0, 0, 0, time.UTC)
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

func TestParseHourlyCutoverUTC(t *testing.T) {
	got := ParseHourlyCutoverUTC("2026-08-11T12:00:00Z")
	want := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ParseHourlyCutoverUTC()=%s want %s", got, want)
	}
	if !ParseHourlyCutoverUTC("").IsZero() {
		t.Fatal("empty cutover must be zero")
	}
}

func TestValidateCutoverPlanApplyRejectsDefaultRows(t *testing.T) {
	err := ValidateCutoverPlanApply(CutoverInventory{DefaultRowCount: 3, CoveredFutureHours: 72, RequiredFutureHours: 72})
	if err == nil {
		t.Fatal("expected DEFAULT rows to block apply")
	}
}

func TestValidateCutoverPlanApplyRequiresCoverage(t *testing.T) {
	err := ValidateCutoverPlanApply(CutoverInventory{CoveredFutureHours: 10, RequiredFutureHours: 72})
	if err == nil {
		t.Fatal("expected insufficient coverage to block apply")
	}
}

func TestRetentionBoundaryMatchesPgPartition(t *testing.T) {
	anchor := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)
	if !pgpartition.RetentionBoundary(anchor).Equal(pgpartition.RetentionBoundary(anchor)) {
		t.Fatal("lifecycle retention boundary must stay aligned with pgpartition helper")
	}
}
