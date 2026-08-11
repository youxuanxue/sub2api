package lifecycle

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
)

// ApplyCutoverPlan provisions [T0,T0+horizon) hourly children under the lifecycle lock.
// It does not mutate application config; operators must set qa_archive.hourly_storage_cutover_utc separately.
func ApplyCutoverPlan(ctx context.Context, db DB, plan CutoverPlan) error {
	if err := ValidateCutoverPlanApply(plan.Inventory); err != nil {
		return err
	}
	recomputed, err := hashCutoverPlan(plan)
	if err != nil {
		return err
	}
	if recomputed != plan.PlanHash {
		return fmt.Errorf("lifecycle: cutover plan hash drift")
	}
	t0 := pgpartition.HourStartUTC(plan.T0UTC)
	if t0.IsZero() {
		return fmt.Errorf("lifecycle: cutover T0 is required")
	}
	horizon := plan.HorizonHours
	if horizon <= 0 {
		horizon = HourlyHorizon
	}
	if err := pgpartition.EnsureHourly(ctx, db, TableQARecords, t0, horizon); err != nil {
		return fmt.Errorf("lifecycle: provision hourly cutover window: %w", err)
	}
	ranges := pgpartition.HourlyTargetRanges(t0, horizon)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, db, TableQARecords, ranges)
	if err != nil {
		return err
	}
	if covered != len(ranges) {
		return fmt.Errorf("lifecycle: cutover coverage %d/%d insufficient after provision", covered, len(ranges))
	}
	if err := ValidateDefaultRemoval(ctx, db); err != nil {
		return err
	}
	defaultName, hasDefault, err := pgpartition.DefaultChildPartition(ctx, db, TableQARecords)
	if err != nil {
		return err
	}
	if hasDefault {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+pq.QuoteIdentifier(defaultName)); err != nil {
			return fmt.Errorf("lifecycle: drop empty DEFAULT partition: %w", err)
		}
	}
	return nil
}
