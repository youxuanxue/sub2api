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

// CutoverPlan is the guarded hourly cutover apply payload.
type CutoverPlan struct {
	SchemaVersion string           `json:"schema_version"`
	PlanHash      string           `json:"plan_hash"`
	T0UTC         time.Time        `json:"t0_utc"`
	HorizonHours  int              `json:"horizon_hours"`
	Inventory     CutoverInventory `json:"inventory"`
}

// BuildCutoverPlan binds inventory facts to an exact T0 hour.
func BuildCutoverPlan(inv CutoverInventory, t0 time.Time) (CutoverPlan, error) {
	t0 = pgpartition.HourStartUTC(t0)
	if t0.IsZero() {
		return CutoverPlan{}, fmt.Errorf("lifecycle: cutover T0 is required")
	}
	plan := CutoverPlan{
		SchemaVersion: "qa-hourly-cutover-plan-v1",
		T0UTC:         t0,
		HorizonHours:  inv.HourlyHorizonHours,
		Inventory:     inv,
	}
	hash, err := hashCutoverPlan(plan)
	if err != nil {
		return CutoverPlan{}, err
	}
	plan.PlanHash = hash
	return plan, nil
}

func hashCutoverPlan(plan CutoverPlan) (string, error) {
	payload := struct {
		SchemaVersion string           `json:"schema_version"`
		T0UTC         time.Time        `json:"t0_utc"`
		HorizonHours  int              `json:"horizon_hours"`
		Inventory     CutoverInventory `json:"inventory"`
	}{
		SchemaVersion: plan.SchemaVersion,
		T0UTC:         plan.T0UTC,
		HorizonHours:  plan.HorizonHours,
		Inventory:     plan.Inventory,
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

// RequiredCutoverConfirmation returns the exact confirmation token for a plan hash.
func RequiredCutoverConfirmation(planHash string) string {
	return cutoverApplyConfirmPrefix + strings.TrimSpace(planHash)
}
