//go:build unit

package lifecycle

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa/archive"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

func TestRetentionUntilForHourUsesHourStartPlus25Hours(t *testing.T) {
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
	if _, err := ParseHourlyCutoverUTCStrict("2026-08-11T12:30:00Z"); err == nil {
		t.Fatal("non-hour cutover must fail")
	}
}

func TestBuildCutoverActivationPlanAllowsDefaultToDrain(t *testing.T) {
	anchor := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	plan, err := BuildCutoverPlan(CutoverInventory{
		DBAnchorUTC: anchor, DefaultPresent: true, DefaultRowCount: 3,
		HourlyHorizonHours: 72,
	}, anchor.Add(time.Hour))
	if err != nil {
		t.Fatalf("BuildCutoverPlan() err=%v", err)
	}
	if plan.Phase != CutoverPhaseActivate {
		t.Fatalf("phase=%q", plan.Phase)
	}
}

func TestBuildCutoverActivationPlanRejectsNonemptyOverlappingMonthly(t *testing.T) {
	anchor := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	_, err := BuildCutoverPlan(CutoverInventory{
		DBAnchorUTC: anchor, HourlyHorizonHours: 72,
		Partitions: []InventoryRow{{
			Schema: "public", Name: "qa_records_202608", Layout: "monthly", RowCount: 1,
			Lower: anchor.Add(-24 * time.Hour), Upper: anchor.Add(30 * 24 * time.Hour),
		}},
	}, anchor.Add(time.Hour))
	if err == nil {
		t.Fatal("nonempty overlapping monthly child must block activation")
	}
}

func TestBuildCutoverFinalizePlanRequiresDrainAndT0Plus25Hours(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 13, 0, 0, 0, time.UTC)
	base := CutoverInventory{
		DBAnchorUTC: t0.Add(25 * time.Hour), DefaultPresent: true,
		HourlyHorizonHours: 72, CoveredFutureHours: 72, RequiredFutureHours: 72,
		ArchiveHeartbeatHealthy: true,
	}
	plan, err := BuildCutoverFinalizePlan(base, t0)
	if err != nil {
		t.Fatalf("BuildCutoverFinalizePlan() err=%v", err)
	}
	if plan.Phase != CutoverPhaseFinalize {
		t.Fatalf("phase=%q", plan.Phase)
	}

	tooEarly := base
	tooEarly.DBAnchorUTC = t0.Add(24 * time.Hour)
	if _, err := BuildCutoverFinalizePlan(tooEarly, t0); err == nil {
		t.Fatal("finalize before T0+25h must fail")
	}
	nonempty := base
	nonempty.DefaultRowCount = 1
	if _, err := BuildCutoverFinalizePlan(nonempty, t0); err == nil {
		t.Fatal("nonempty DEFAULT must block finalize")
	}
	missingDefault := base
	missingDefault.DefaultPresent = false
	if _, err := BuildCutoverFinalizePlan(missingDefault, t0); err == nil {
		t.Fatal("missing DEFAULT before finalize must fail closed")
	}
	legacy := base
	legacy.Partitions = []InventoryRow{{Schema: "public", Name: "qa_records_202608", Layout: "monthly"}}
	if _, err := BuildCutoverFinalizePlan(legacy, t0); err == nil {
		t.Fatal("legacy child must block finalize")
	}
	hotFiles := base
	hotFiles.LegacyBlobFiles = 1
	if _, err := BuildCutoverFinalizePlan(hotFiles, t0); err == nil {
		t.Fatal("legacy hot files must block finalize")
	}
	unhealthy := base
	unhealthy.ArchiveHeartbeatHealthy = false
	if _, err := BuildCutoverFinalizePlan(unhealthy, t0); err == nil {
		t.Fatal("unhealthy archive heartbeat must block finalize")
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
		DBAnchorUTC:        time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC),
		HourlyHorizonHours: 72,
	}
	t0 := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	plan, err := BuildCutoverPlan(inv, t0)
	if err != nil {
		t.Fatalf("BuildCutoverPlan() err=%v", err)
	}
	if len(plan.PlanHash) != 64 {
		t.Fatalf("plan hash len=%d", len(plan.PlanHash))
	}
}

func TestBuildCutoverActivationPlanHashIgnoresDrainingDefaultRows(t *testing.T) {
	anchor := time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC)
	t0 := anchor.Add(time.Hour)
	inv := CutoverInventory{
		DBAnchorUTC: anchor, HourlyHorizonHours: HourlyHorizon,
		DefaultPresent: true, DefaultRowCount: 10,
		Partitions: []InventoryRow{{Schema: "public", Name: "qa_records_default", IsDefault: true, Layout: "default", RowCount: 10}},
	}
	first, err := BuildCutoverPlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv.DefaultRowCount = 11
	inv.Partitions[0].RowCount = 11
	second, err := BuildCutoverPlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash != second.PlanHash {
		t.Fatal("draining DEFAULT writes must not invalidate an activation plan")
	}
}

func TestBuildCutoverFinalizePlanHashIgnoresActiveHourlyRows(t *testing.T) {
	t0 := time.Date(2026, 8, 11, 11, 0, 0, 0, time.UTC)
	inv := CutoverInventory{
		DBAnchorUTC: t0.Add(25 * time.Hour), HourlyHorizonHours: HourlyHorizon,
		CoveredFutureHours: HourlyHorizon, RequiredFutureHours: HourlyHorizon,
		DefaultPresent: true, ArchiveHeartbeatHealthy: true,
		Partitions: []InventoryRow{{Schema: "public", Name: "qa_records_20260812_12", Layout: "hourly", RowCount: 10}},
	}
	first, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}
	inv.Partitions[0].RowCount = 11
	second, err := BuildCutoverFinalizePlan(inv, t0)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash != second.PlanHash {
		t.Fatal("active hourly writes must not invalidate a finalize plan")
	}
}
