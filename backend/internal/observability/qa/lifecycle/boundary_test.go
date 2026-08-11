//go:build unit

package lifecycle

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
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
	want := pgpartition.RetentionBoundary(anchor)
	if got := pgpartition.RetentionBoundary(anchor); !got.Equal(want) {
		t.Fatalf("RetentionBoundary()=%s want %s", got, want)
	}
}

func TestHourlyTargetRangeCountIsExclusiveHorizon(t *testing.T) {
	ranges := pgpartition.HourlyTargetRanges(time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC), HourlyHorizon)
	if len(ranges) != HourlyHorizon {
		t.Fatalf("HourlyTargetRanges() len=%d want %d", len(ranges), HourlyHorizon)
	}
}

func TestNeedsBoundaryTerminalGapRequiresMembershipCoverage(t *testing.T) {
	if !needsBoundaryTerminalGap(archive.CatchupHourStatus{
		Exists: true, State: archive.StateCommitted, RestoreVerified: true, UncoveredSourceExists: true,
	}) {
		t.Fatal("uncovered committed hour must require terminal gap")
	}
	if needsBoundaryTerminalGap(archive.CatchupHourStatus{
		Exists: true, State: archive.StateCommitted, RestoreVerified: true,
	}) {
		t.Fatal("fully covered committed hour must not require terminal gap")
	}
}

func TestBuildCutoverPlanHashesInventory(t *testing.T) {
	inv := CutoverInventory{
		HourlyHorizonHours:  72,
		CoveredFutureHours:  72,
		RequiredFutureHours: 72,
	}
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	plan, err := BuildCutoverPlan(inv, t0)
	if err != nil {
		t.Fatalf("BuildCutoverPlan() err=%v", err)
	}
	if len(plan.PlanHash) != 64 {
		t.Fatalf("plan hash len=%d", len(plan.PlanHash))
	}
	if RequiredCutoverConfirmation(plan.PlanHash) == "" {
		t.Fatal("confirmation token missing")
	}
}
