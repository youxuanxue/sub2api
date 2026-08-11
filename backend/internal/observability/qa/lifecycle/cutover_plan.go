package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pgpartition"
)

const cutoverApplyConfirmPrefix = "tokenkey-prod-qa-cutover-apply-v1:"

type CutoverPhase string

const (
	CutoverPhaseActivate CutoverPhase = "activate"
	CutoverPhaseFinalize CutoverPhase = "finalize"
)

// CutoverPlan is the guarded hourly cutover apply payload.
type CutoverPlan struct {
	SchemaVersion string           `json:"schema_version"`
	PlanHash      string           `json:"plan_hash"`
	Phase         CutoverPhase     `json:"phase"`
	T0UTC         time.Time        `json:"t0_utc"`
	HorizonHours  int              `json:"horizon_hours"`
	DropMonthly   []InventoryRow   `json:"drop_empty_monthly,omitempty"`
	Inventory     CutoverInventory `json:"inventory"`
}

// BuildCutoverPlan binds inventory facts to an exact T0 hour.
func BuildCutoverPlan(inv CutoverInventory, t0 time.Time) (CutoverPlan, error) {
	t0, err := validatePlanT0(t0)
	if err != nil {
		return CutoverPlan{}, err
	}
	if inv.DBAnchorUTC.IsZero() || !t0.After(pgpartition.HourStartUTC(inv.DBAnchorUTC)) {
		return CutoverPlan{}, fmt.Errorf("lifecycle: activation T0 must be after the database anchor hour")
	}
	horizon := inv.HourlyHorizonHours
	if horizon <= 0 {
		horizon = HourlyHorizon
	}
	windowEnd := t0.Add(time.Duration(horizon) * time.Hour)
	dropMonthly := make([]InventoryRow, 0)
	for _, row := range inv.Partitions {
		if row.Layout != "monthly" || !rangesOverlap(row.Lower, row.Upper, t0, windowEnd) {
			continue
		}
		if row.RowCount != 0 {
			return CutoverPlan{}, fmt.Errorf("lifecycle: overlapping monthly partition %s.%s still holds %d rows", row.Schema, row.Name, row.RowCount)
		}
		dropMonthly = append(dropMonthly, row)
	}
	plan := CutoverPlan{
		SchemaVersion: "qa-hourly-cutover-plan-v2",
		Phase:         CutoverPhaseActivate,
		T0UTC:         t0,
		HorizonHours:  horizon,
		DropMonthly:   dropMonthly,
		Inventory:     inv,
	}
	hash, err := hashCutoverPlan(plan)
	if err != nil {
		return CutoverPlan{}, err
	}
	plan.PlanHash = hash
	return plan, nil
}

// BuildCutoverFinalizePlan binds the destructive DEFAULT-removal gate to drained facts.
func BuildCutoverFinalizePlan(inv CutoverInventory, t0 time.Time) (CutoverPlan, error) {
	t0, err := validatePlanT0(t0)
	if err != nil {
		return CutoverPlan{}, err
	}
	if inv.DBAnchorUTC.Before(t0.Add(25 * time.Hour)) {
		return CutoverPlan{}, fmt.Errorf("lifecycle: finalize requires at least 25 hours after T0")
	}
	if !inv.DefaultPresent {
		return CutoverPlan{}, fmt.Errorf("lifecycle: DEFAULT is missing before finalize")
	}
	if inv.DefaultRowCount != 0 {
		return CutoverPlan{}, fmt.Errorf("lifecycle: DEFAULT still holds %d rows", inv.DefaultRowCount)
	}
	for _, row := range inv.Partitions {
		if row.IsDefault || row.Layout == "hourly" {
			continue
		}
		return CutoverPlan{}, fmt.Errorf("lifecycle: legacy partition %s.%s remains during finalize", row.Schema, row.Name)
	}
	if inv.CoveredFutureHours != inv.RequiredFutureHours || inv.RequiredFutureHours != HourlyHorizon {
		return CutoverPlan{}, fmt.Errorf("lifecycle: future hourly coverage %d/%d insufficient for finalize", inv.CoveredFutureHours, inv.RequiredFutureHours)
	}
	if inv.LegacyBlobFiles != 0 || inv.LegacyDLQFiles != 0 {
		return CutoverPlan{}, fmt.Errorf(
			"lifecycle: legacy hot files remain: blobs=%d dlq=%d",
			inv.LegacyBlobFiles,
			inv.LegacyDLQFiles,
		)
	}
	if !inv.ArchiveHeartbeatHealthy {
		return CutoverPlan{}, fmt.Errorf("lifecycle: archive heartbeat is not a fresh success")
	}
	plan := CutoverPlan{
		SchemaVersion: "qa-hourly-cutover-finalize-plan-v1",
		Phase:         CutoverPhaseFinalize,
		T0UTC:         t0,
		HorizonHours:  HourlyHorizon,
		Inventory:     inv,
	}
	hash, err := hashCutoverPlan(plan)
	if err != nil {
		return CutoverPlan{}, err
	}
	plan.PlanHash = hash
	return plan, nil
}

func validatePlanT0(t0 time.Time) (time.Time, error) {
	if t0.IsZero() {
		return time.Time{}, fmt.Errorf("lifecycle: cutover T0 is required")
	}
	t0 = t0.UTC()
	if !t0.Equal(pgpartition.HourStartUTC(t0)) {
		return time.Time{}, fmt.Errorf("lifecycle: cutover T0 must be an exact UTC hour")
	}
	return t0, nil
}

func rangesOverlap(lower, upper, start, end time.Time) bool {
	return !lower.IsZero() && !upper.IsZero() && lower.Before(end) && upper.After(start)
}

func hashCutoverPlan(plan CutoverPlan) (string, error) {
	type activationFacts struct {
		DBAnchorUTC time.Time      `json:"db_anchor_utc"`
		DropMonthly []InventoryRow `json:"drop_empty_monthly,omitempty"`
	}
	type finalizeFacts struct {
		DBAnchorUTC             time.Time `json:"db_anchor_utc"`
		CoveredFutureHours      int       `json:"covered_future_hours"`
		RequiredFutureHours     int       `json:"required_future_hours"`
		DefaultPresent          bool      `json:"default_present"`
		DefaultRowCount         int64     `json:"default_row_count"`
		LegacyBlobFiles         int64     `json:"legacy_blob_files"`
		LegacyDLQFiles          int64     `json:"legacy_dlq_files"`
		ArchiveHeartbeatHealthy bool      `json:"archive_heartbeat_healthy"`
	}
	payload := struct {
		SchemaVersion string           `json:"schema_version"`
		Phase         CutoverPhase     `json:"phase"`
		T0UTC         time.Time        `json:"t0_utc"`
		HorizonHours  int              `json:"horizon_hours"`
		Activation    *activationFacts `json:"activation,omitempty"`
		Finalize      *finalizeFacts   `json:"finalize,omitempty"`
	}{
		SchemaVersion: plan.SchemaVersion,
		Phase:         plan.Phase,
		T0UTC:         plan.T0UTC,
		HorizonHours:  plan.HorizonHours,
	}
	switch plan.Phase {
	case CutoverPhaseActivate:
		payload.Activation = &activationFacts{
			DBAnchorUTC: plan.Inventory.DBAnchorUTC,
			DropMonthly: plan.DropMonthly,
		}
	case CutoverPhaseFinalize:
		payload.Finalize = &finalizeFacts{
			DBAnchorUTC:             plan.Inventory.DBAnchorUTC,
			CoveredFutureHours:      plan.Inventory.CoveredFutureHours,
			RequiredFutureHours:     plan.Inventory.RequiredFutureHours,
			DefaultPresent:          plan.Inventory.DefaultPresent,
			DefaultRowCount:         plan.Inventory.DefaultRowCount,
			LegacyBlobFiles:         plan.Inventory.LegacyBlobFiles,
			LegacyDLQFiles:          plan.Inventory.LegacyDLQFiles,
			ArchiveHeartbeatHealthy: plan.Inventory.ArchiveHeartbeatHealthy,
		}
	default:
		return "", fmt.Errorf("lifecycle: unsupported cutover phase %q", plan.Phase)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("lifecycle: hash cutover plan: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// ValidateCutoverApplyConfirmation checks the operator confirmation token.
func ValidateCutoverApplyConfirmation(planHash, confirmation string) error {
	planHash = strings.TrimSpace(planHash)
	confirmation = strings.TrimSpace(confirmation)
	want := cutoverApplyConfirmPrefix + planHash
	if confirmation != want {
		return fmt.Errorf("lifecycle: cutover apply confirmation mismatch")
	}
	if len(planHash) != 64 {
		return fmt.Errorf("lifecycle: cutover plan hash is invalid")
	}
	return nil
}
