package service

import (
	"testing"
	"time"
)

// TestEstimateSetupTokenUsage_StaleSampleGuard verifies that a
// session_window_utilization sample taken before the current window started is
// treated as stale (dropped → status-based fallback), while a sample taken
// within the window is used as-is. Guards the "99% on a just-rolled window" bug.
func TestEstimateSetupTokenUsage_StaleSampleGuard(t *testing.T) {
	now := time.Now()
	wStart := now.Add(-1 * time.Hour)
	wEnd := now.Add(4 * time.Hour)

	mk := func(sampledAt *time.Time) *Account {
		extra := map[string]any{"session_window_utilization": 0.99}
		if sampledAt != nil {
			extra["passive_usage_sampled_at"] = sampledAt.UTC().Format(time.RFC3339)
		}
		return &Account{
			SessionWindowStart:  &wStart,
			SessionWindowEnd:    &wEnd,
			SessionWindowStatus: "allowed",
			Extra:               extra,
		}
	}

	svc := &AccountUsageService{}

	t.Run("stale sample (before window start) is dropped", func(t *testing.T) {
		sampled := wStart.Add(-1 * time.Hour) // before window start → stale
		got := svc.estimateSetupTokenUsage(mk(&sampled))
		if got.FiveHour == nil {
			t.Fatal("FiveHour nil")
		}
		// status=allowed → status-based fallback is 0, NOT the stale 99.
		if got.FiveHour.Utilization != 0 {
			t.Fatalf("expected stale util dropped to 0, got %v", got.FiveHour.Utilization)
		}
	})

	t.Run("fresh sample (within window) is used", func(t *testing.T) {
		sampled := wStart.Add(30 * time.Minute) // after window start → fresh
		got := svc.estimateSetupTokenUsage(mk(&sampled))
		if got.FiveHour == nil || got.FiveHour.Utilization != 99 {
			t.Fatalf("expected fresh util 99, got %+v", got.FiveHour)
		}
	})

	t.Run("no sampled_at falls back to using util (backward compatible)", func(t *testing.T) {
		got := svc.estimateSetupTokenUsage(mk(nil))
		if got.FiveHour == nil || got.FiveHour.Utilization != 99 {
			t.Fatalf("expected util 99 when sampled_at absent, got %+v", got.FiveHour)
		}
	})

	t.Run("no window start leaves util untouched", func(t *testing.T) {
		sampled := now.Add(-3 * time.Hour)
		acc := mk(&sampled)
		acc.SessionWindowStart = nil // can't verify → use util as-is
		got := svc.estimateSetupTokenUsage(acc)
		if got.FiveHour == nil || got.FiveHour.Utilization != 99 {
			t.Fatalf("expected util 99 when window start unknown, got %+v", got.FiveHour)
		}
	})
}
