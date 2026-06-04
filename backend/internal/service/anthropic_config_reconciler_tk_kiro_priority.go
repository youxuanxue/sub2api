package service

import (
	"context"
	"log/slog"

	kiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

// reconcileKiroPriorityBaseline HARD-ENFORCES every active kiro account's priority
// column to kiro.DefaultKiroAccountPriority (value-sync, skip-if-aligned) — the same
// shape as Step T's tier-concurrency value-sync. This is a kiro-scoped concept: kiro
// schedules in its own isolated pool and is NOT in the anthropic window-rebalance
// pipeline, so writing kiro priority here does NOT violate the reconciler's rule that
// it never writes anthropic priority. BulkUpdate auto-enqueues a scheduler_outbox
// event so the live snapshot picks up the corrected priority.
//
// It lives on the anthropic-named reconciler purely to reuse its ticker + redis leader
// lock + account store + BulkUpdate outbox path (most minimal; no new goroutine or
// wiring). It fetches its OWN kiro account list — runOnce's list is anthropic-only.
func (r *AnthropicConfigReconciler) reconcileKiroPriorityBaseline(ctx context.Context) {
	if r == nil || r.accounts == nil {
		return
	}
	accounts, err := r.accounts.ListByPlatform(ctx, PlatformKiro)
	if err != nil {
		slog.Warn("anthropic config reconciler: list kiro accounts failed (priority baseline)", "err", err)
		return
	}
	const want = kiro.DefaultKiroAccountPriority
	for i := range accounts {
		a := &accounts[i]
		if !a.IsKiro() { // belt-and-suspenders; ListByPlatform already filters
			continue
		}
		if a.Priority == want {
			continue // already aligned
		}
		w := want
		if _, err := r.accounts.BulkUpdate(ctx, []int64{a.ID}, AccountBulkUpdate{Priority: &w}); err != nil {
			slog.Warn("anthropic config reconciler: kiro priority baseline value-sync write failed",
				"account_id", a.ID, "account_name", a.Name, "want", want, "err", err)
			continue
		}
		slog.Info("anthropic config reconciler: kiro priority baseline value-synced (local deployment only)",
			"account_id", a.ID, "account_name", a.Name, "priority", want)
	}
}
