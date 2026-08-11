package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

// CutoverInventory summarizes read-only facts for guarded hourly cutover planning.
type CutoverInventory struct {
	DBAnchorUTC          time.Time      `json:"db_anchor_utc"`
	RetentionBoundaryUTC time.Time      `json:"retention_boundary_utc"`
	HourlyHorizonHours   int            `json:"hourly_horizon_hours"`
	CoveredFutureHours   int            `json:"covered_future_hours"`
	RequiredFutureHours  int            `json:"required_future_hours"`
	DefaultPresent       bool           `json:"default_present"`
	DefaultRowCount      int64          `json:"default_row_count"`
	OverlappingMonthly   []InventoryRow `json:"overlapping_monthly"`
	Partitions           []InventoryRow `json:"partitions"`
}

// BuildCutoverInventory collects partition catalog facts without mutating state.
func BuildCutoverInventory(ctx context.Context, db DB, horizon int) (CutoverInventory, error) {
	if horizon <= 0 {
		horizon = HourlyHorizon
	}
	anchor, err := DatabaseUTC(ctx, db)
	if err != nil {
		return CutoverInventory{}, err
	}
	boundary := pgpartition.RetentionBoundary(anchor)
	rows, err := Inventory(ctx, db, anchor)
	if err != nil {
		return CutoverInventory{}, err
	}
	out := CutoverInventory{
		DBAnchorUTC:          anchor,
		RetentionBoundaryUTC: boundary,
		HourlyHorizonHours:   horizon,
		RequiredFutureHours:  horizon,
		Partitions:           rows,
	}
	ranges := pgpartition.HourlyTargetRanges(anchor, horizon)
	covered, err := pgpartition.CountCoveredHourlyRanges(ctx, db, TableQARecords, ranges)
	if err != nil {
		return CutoverInventory{}, err
	}
	out.CoveredFutureHours = covered
	for _, row := range rows {
		if row.IsDefault {
			out.DefaultPresent = true
			out.DefaultRowCount = row.RowCount
			continue
		}
		if row.Layout == "monthly" && row.RowCount > 0 {
			out.OverlappingMonthly = append(out.OverlappingMonthly, row)
		}
	}
	return out, nil
}

// EncodeCutoverInventory renders inventory as indented JSON.
func EncodeCutoverInventory(out CutoverInventory) ([]byte, error) {
	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("lifecycle: encode cutover inventory: %w", err)
	}
	return payload, nil
}

// ValidateCutoverPlanApply rejects guarded apply preconditions (read-only gate).
func ValidateCutoverPlanApply(inv CutoverInventory) error {
	if inv.DefaultRowCount > 0 {
		return fmt.Errorf("lifecycle: DEFAULT still holds %d rows", inv.DefaultRowCount)
	}
	if len(inv.OverlappingMonthly) > 0 {
		return fmt.Errorf("lifecycle: %d overlapping monthly partitions still hold rows", len(inv.OverlappingMonthly))
	}
	if inv.CoveredFutureHours < inv.RequiredFutureHours {
		return fmt.Errorf("lifecycle: future hourly coverage %d/%d insufficient for apply",
			inv.CoveredFutureHours, inv.RequiredFutureHours)
	}
	return nil
}
