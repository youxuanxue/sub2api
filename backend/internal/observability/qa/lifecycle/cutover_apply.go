package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
	"github.com/lib/pq"
)

const HourlyStorageCutoverSettingKey = "qa_hourly_storage_cutover_utc"

// MatchAppliedCutover handles receipt-first idempotent CLI replay.
func MatchAppliedCutover(
	ctx context.Context,
	db DB,
	phase CutoverPhase,
	t0 time.Time,
	planHash string,
) (bool, error) {
	if phase != CutoverPhaseActivate && phase != CutoverPhaseFinalize {
		return false, fmt.Errorf("lifecycle: unsupported cutover phase %q", phase)
	}
	t0, err := validatePlanT0(t0)
	if err != nil {
		return false, err
	}
	var appliedHash string
	var appliedT0 time.Time
	err = db.QueryRowContext(ctx, `
SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts WHERE phase=$1`, phase).
		Scan(&appliedHash, &appliedT0)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lifecycle: read %s receipt: %w", phase, err)
	}
	if appliedHash != strings.TrimSpace(planHash) || !appliedT0.UTC().Equal(t0) {
		return false, fmt.Errorf("lifecycle: cutover phase %s was already applied with different facts", phase)
	}
	return true, nil
}

// ApplyCutoverPlan atomically applies one hash-bound activation or finalization plan.
func ApplyCutoverPlan(ctx context.Context, db *sql.DB, plan CutoverPlan) error {
	recomputed, err := hashCutoverPlan(plan)
	if err != nil {
		return err
	}
	if recomputed != plan.PlanHash {
		return fmt.Errorf("lifecycle: cutover plan hash drift")
	}
	if _, err := validatePlanT0(plan.T0UTC); err != nil {
		return err
	}
	if plan.Phase != CutoverPhaseActivate && plan.Phase != CutoverPhaseFinalize {
		return fmt.Errorf("lifecycle: unsupported cutover phase %q", plan.Phase)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("lifecycle: begin cutover transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE "qa_records" IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lifecycle: lock qa_records for cutover: %w", err)
	}

	applied, err := readAppliedCutover(ctx, tx, plan.Phase)
	if err != nil {
		return err
	}
	if plan.Phase == CutoverPhaseFinalize {
		activation, err := readAppliedCutover(ctx, tx, CutoverPhaseActivate)
		if err != nil {
			return err
		}
		if activation == nil {
			return fmt.Errorf("lifecycle: finalize requires an activation receipt")
		}
		if !activation.T0UTC.Equal(plan.T0UTC) {
			return fmt.Errorf("lifecycle: finalize T0 does not match the activation receipt")
		}
	}
	if applied != nil {
		if applied.PlanHash != plan.PlanHash || !applied.T0UTC.Equal(plan.T0UTC) {
			return fmt.Errorf("lifecycle: cutover phase %s was already applied with different facts", plan.Phase)
		}
		return tx.Commit()
	}

	switch plan.Phase {
	case CutoverPhaseActivate:
		err = applyCutoverActivation(ctx, tx, plan)
	case CutoverPhaseFinalize:
		err = applyCutoverFinalize(ctx, tx, plan)
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO qa_lifecycle_receipts (phase, plan_hash, t0_utc)
VALUES ($1,$2,$3)`, plan.Phase, plan.PlanHash, plan.T0UTC); err != nil {
		return fmt.Errorf("lifecycle: persist %s receipt: %w", plan.Phase, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("lifecycle: commit %s: %w", plan.Phase, err)
	}
	return nil
}

type appliedCutover struct {
	PlanHash string
	T0UTC    time.Time
}

func readAppliedCutover(ctx context.Context, tx *sql.Tx, phase CutoverPhase) (*appliedCutover, error) {
	var out appliedCutover
	err := tx.QueryRowContext(ctx, `
SELECT plan_hash, t0_utc FROM qa_lifecycle_receipts WHERE phase=$1`, phase).Scan(&out.PlanHash, &out.T0UTC)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lifecycle: read %s receipt: %w", phase, err)
	}
	out.T0UTC = out.T0UTC.UTC()
	return &out, nil
}

func applyCutoverActivation(ctx context.Context, tx *sql.Tx, plan CutoverPlan) error {
	anchor, err := DatabaseUTC(ctx, tx)
	if err != nil {
		return err
	}
	if !plan.T0UTC.After(anchor) {
		return fmt.Errorf("lifecycle: activation T0 is no longer in the future")
	}
	if plan.HorizonHours != HourlyHorizon {
		return fmt.Errorf("lifecycle: activation horizon must be %d hours", HourlyHorizon)
	}

	children, err := pgpartition.ListInventoryChildBounds(ctx, tx, TableQARecords)
	if err != nil {
		return err
	}
	wantDrops := make(map[string]InventoryRow, len(plan.DropMonthly))
	for _, row := range plan.DropMonthly {
		wantDrops[row.Schema+"\x00"+row.Name] = row
	}
	foundDrops := make(map[string]pgpartition.InventoryChildBound, len(plan.DropMonthly))
	windowEnd := plan.T0UTC.Add(time.Duration(plan.HorizonHours) * time.Hour)
	for _, child := range children {
		if child.Layout != "monthly" || !rangesOverlap(child.Lower, child.Upper, plan.T0UTC, windowEnd) {
			continue
		}
		count, err := pgpartition.CountTableRows(ctx, tx, child.Schema, child.Name)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("lifecycle: overlapping monthly partition %s.%s now holds %d rows", child.Schema, child.Name, count)
		}
		key := child.Schema + "\x00" + child.Name
		planned, ok := wantDrops[key]
		if !ok || !planned.Lower.Equal(child.Lower) || !planned.Upper.Equal(child.Upper) {
			return fmt.Errorf("lifecycle: overlapping monthly partition inventory drift")
		}
		foundDrops[key] = child
	}
	if len(foundDrops) != len(wantDrops) {
		return fmt.Errorf("lifecycle: planned monthly drop inventory drift")
	}
	for _, planned := range plan.DropMonthly {
		child := foundDrops[planned.Schema+"\x00"+planned.Name]
		if err := pgpartition.DropChildPartition(ctx, tx, pgpartition.ChildPartitionBound{
			Schema: child.Schema, Name: child.Name, Lower: child.Lower, Upper: child.Upper,
		}); err != nil {
			return fmt.Errorf("lifecycle: drop empty overlapping monthly child: %w", err)
		}
	}

	if err := pgpartition.EnsureHourly(ctx, tx, TableQARecords, plan.T0UTC, plan.HorizonHours); err != nil {
		return fmt.Errorf("lifecycle: provision hourly cutover window: %w", err)
	}
	ranges := pgpartition.HourlyTargetRanges(plan.T0UTC, plan.HorizonHours)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, tx, TableQARecords, ranges)
	if err != nil {
		return err
	}
	if covered != len(ranges) {
		return fmt.Errorf("lifecycle: cutover coverage %d/%d insufficient after provision", covered, len(ranges))
	}
	return persistHourlyStorageCutover(ctx, tx, plan.T0UTC)
}

func persistHourlyStorageCutover(ctx context.Context, tx *sql.Tx, t0 time.Time) error {
	value := t0.UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES ($1,$2,now())
ON CONFLICT (key) DO NOTHING`, HourlyStorageCutoverSettingKey, value); err != nil {
		return fmt.Errorf("lifecycle: persist immutable application T0: %w", err)
	}
	var got string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, HourlyStorageCutoverSettingKey).Scan(&got); err != nil {
		return fmt.Errorf("lifecycle: read immutable application T0: %w", err)
	}
	parsed, err := ParseHourlyCutoverUTCStrict(got)
	if err != nil || !parsed.Equal(t0) {
		return fmt.Errorf("lifecycle: immutable application T0 conflicts with activation plan")
	}
	return nil
}

func applyCutoverFinalize(ctx context.Context, tx *sql.Tx, plan CutoverPlan) error {
	anchor, err := DatabaseUTC(ctx, tx)
	if err != nil {
		return err
	}
	if anchor.Before(plan.T0UTC.Add(25 * time.Hour)) {
		return fmt.Errorf("lifecycle: finalize requires at least 25 hours after T0")
	}
	if err := persistHourlyStorageCutover(ctx, tx, plan.T0UTC); err != nil {
		return err
	}
	children, err := pgpartition.ListInventoryChildBounds(ctx, tx, TableQARecords)
	if err != nil {
		return err
	}
	wantDrops := make(map[string]InventoryRow, len(plan.DropMonthly))
	for _, row := range plan.DropMonthly {
		if row.Layout != "monthly" || row.RowCount != 0 || row.Lower.IsZero() || row.Upper.IsZero() || !row.Lower.Before(row.Upper) {
			return fmt.Errorf("lifecycle: invalid planned monthly drop %s.%s", row.Schema, row.Name)
		}
		key := row.Schema + "\x00" + row.Name
		if _, exists := wantDrops[key]; exists {
			return fmt.Errorf("lifecycle: duplicate planned monthly drop %s.%s", row.Schema, row.Name)
		}
		wantDrops[key] = row
	}
	foundDrops := make(map[string]pgpartition.InventoryChildBound, len(plan.DropMonthly))
	var defaultChild *pgpartition.InventoryChildBound
	for i := range children {
		child := &children[i]
		if child.IsDefault {
			defaultChild = child
			continue
		}
		if child.Layout != "hourly" {
			if child.Layout != "monthly" {
				return fmt.Errorf("lifecycle: legacy partition %s.%s remains during finalize", child.Schema, child.Name)
			}
			key := child.Schema + "\x00" + child.Name
			planned, ok := wantDrops[key]
			if !ok || !planned.Lower.Equal(child.Lower) || !planned.Upper.Equal(child.Upper) {
				return fmt.Errorf("lifecycle: monthly partition inventory drift")
			}
			count, err := pgpartition.CountTableRows(ctx, tx, child.Schema, child.Name)
			if err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("lifecycle: monthly partition %s.%s now holds %d rows", child.Schema, child.Name, count)
			}
			foundDrops[key] = *child
		}
	}
	if len(foundDrops) != len(wantDrops) {
		return fmt.Errorf("lifecycle: planned monthly drop inventory drift")
	}
	ranges := pgpartition.HourlyTargetRanges(anchor, HourlyHorizon)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, tx, TableQARecords, ranges)
	if err != nil {
		return err
	}
	if covered != len(ranges) {
		return fmt.Errorf("lifecycle: finalize coverage %d/%d insufficient", covered, len(ranges))
	}
	if defaultChild == nil {
		return fmt.Errorf("lifecycle: DEFAULT is missing before finalize")
	}
	count, err := pgpartition.CountTableRows(ctx, tx, defaultChild.Schema, defaultChild.Name)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("lifecycle: DEFAULT partition %s.%s still holds %d rows", defaultChild.Schema, defaultChild.Name, count)
	}
	for _, planned := range plan.DropMonthly {
		child := foundDrops[planned.Schema+"\x00"+planned.Name]
		if err := pgpartition.DropChildPartition(ctx, tx, pgpartition.ChildPartitionBound{
			Schema: child.Schema, Name: child.Name, Lower: child.Lower, Upper: child.Upper,
		}); err != nil {
			return fmt.Errorf("lifecycle: drop empty legacy monthly child: %w", err)
		}
	}
	qualified := pq.QuoteIdentifier(defaultChild.Schema) + "." + pq.QuoteIdentifier(defaultChild.Name)
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		return fmt.Errorf("lifecycle: drop empty DEFAULT partition: %w", err)
	}
	return nil
}
