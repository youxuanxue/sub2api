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
	DBAnchorUTC             time.Time      `json:"db_anchor_utc"`
	RetentionBoundaryUTC    time.Time      `json:"retention_boundary_utc"`
	HourlyHorizonHours      int            `json:"hourly_horizon_hours"`
	CoveredFutureHours      int            `json:"covered_future_hours"`
	RequiredFutureHours     int            `json:"required_future_hours"`
	DefaultPresent          bool           `json:"default_present"`
	DefaultRowCount         int64          `json:"default_row_count"`
	LegacyBlobFiles         int64          `json:"legacy_blob_files"`
	LegacyDLQFiles          int64          `json:"legacy_dlq_files"`
	ArchiveHeartbeatHealthy bool           `json:"archive_heartbeat_healthy"`
	OverlappingMonthly      []InventoryRow `json:"overlapping_monthly"`
	Partitions              []InventoryRow `json:"partitions"`
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
	rows, err := Inventory(ctx, db)
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
	if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM ops_job_heartbeats
  WHERE job_name = 'qa-maintenance'
    AND last_success_at IS NOT NULL
    AND last_success_at >= clock_timestamp() - interval '2 hours'
    AND (last_error_at IS NULL OR last_error_at <= last_success_at)
    AND last_result LIKE 'status=committed %'
)`).Scan(&out.ArchiveHeartbeatHealthy); err != nil {
		return CutoverInventory{}, fmt.Errorf("lifecycle: read archive heartbeat health: %w", err)
	}
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

// AddHotFileInventory binds filesystem drain facts to a cutover inventory.
func AddHotFileInventory(inv *CutoverInventory, dataDir string) error {
	if inv == nil {
		return fmt.Errorf("lifecycle: cutover inventory is nil")
	}
	files, err := InspectLegacyHotFiles(dataDir)
	if err != nil {
		return err
	}
	inv.LegacyBlobFiles = files.BlobFiles
	inv.LegacyDLQFiles = files.DLQFiles
	return nil
}

// EncodeCutoverInventory renders inventory as indented JSON.
func EncodeCutoverInventory(out CutoverInventory) ([]byte, error) {
	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("lifecycle: encode cutover inventory: %w", err)
	}
	return payload, nil
}
