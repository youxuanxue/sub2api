package service

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
)

// reconcileAccountBaselineDrift self-heals only the narrow account-side
// shared_baseline infrastructure for every tier-bound anthropic OAuth/setup-token
// account on THIS node: the canonical TLS profile's existence + the account's
// binding to it, and missing credentials self-protection template keys. It does
// NOT re-apply the full baseline extra/concurrency/priority shape, so normal
// admin UI edits (rpm_strategy, masking, custom base URL, etc.) stay operator-
// owned.
//
// It deliberately does NOT touch priority: priority is a dynamic runtime signal
// owned by the window-rebalance pipeline (ops/anthropic/rebalance-anthropic-
// priority.py); the git baseline seeds it only on the operator-explicit ApplyTier
// path, and reverting it on every tick would flatten the window-aware ordering.
// It also does NOT touch tier NUMERIC fields (base_rpm / max_sessions / window) —
// those overlay at runtime from the tiers table and stay report-only
// (reportTierDrift). It reuses the reconciler's ticker + redis leader lock; the
// narrow repair itself enqueues the snapshot-rebuild outbox event via
// UpdateAccount.
func (r *AnthropicConfigReconciler) reconcileAccountBaselineDrift(ctx context.Context, accounts []Account) {
	if r == nil || r.repairer == nil || r.tiers == nil {
		return // repairer/resolver not wired (minimal test deps) → no-op
	}
	for i := range accounts {
		a := &accounts[i]
		if a.TierID == nil || *a.TierID <= 0 || !a.IsAnthropicOAuthOrSetupToken() {
			continue
		}
		if a.IsAnthropicOAuthPassthroughEnabled() {
			continue
		}
		tierName, ok := r.tiers.ResolveName(*a.TierID)
		if !ok || tierName == "" {
			continue
		}
		eff, err := baseline.EffectiveBaselineForTier(tierName)
		if err != nil {
			slog.Warn("anthropic config reconciler: load tier baseline failed (account baseline self-heal)",
				"account_id", a.ID, "tier", tierName, "err", err)
			continue
		}
		reason := r.accountBaselineDriftReason(ctx, a, eff)
		if reason == "" {
			continue // aligned → skip (no write)
		}
		if _, err := r.repairer.RepairBaselineDrift(ctx, a.ID, tierName); err != nil {
			slog.Warn("anthropic config reconciler: account baseline narrow repair failed",
				"account_id", a.ID, "account_name", a.Name, "tier", tierName, "reason", reason, "err", err)
			continue
		}
		slog.Info("anthropic config reconciler: account baseline narrow-repaired (local deployment only)",
			"account_id", a.ID, "account_name", a.Name, "tier", tierName, "reason", reason)
	}
}

// accountBaselineDriftReason returns a short reason string when the account drifts
// from the shared_baseline infrastructure, or "" when aligned. Checks are
// account-side (cheap) plus an optional dangling-binding lookup; on a TLS resolver
// error it conservatively does NOT report drift (never flap on a transient read).
func (r *AnthropicConfigReconciler) accountBaselineDriftReason(ctx context.Context, a *Account, eff *baseline.EffectiveTierBaseline) string {
	// priority is intentionally NOT checked here: it is a dynamic runtime signal
	// owned by the window-rebalance pipeline. Self-healing it on every tick would
	// flatten rebalance's window-aware ordering back to the uniform baseline. It is
	// seeded from the git baseline only on the operator-explicit ApplyTier path.
	// enable_tls_fingerprint flag must be on.
	if v, _ := a.Extra["enable_tls_fingerprint"].(bool); !v {
		return "tls_fingerprint_disabled"
	}
	// TLS binding: id must be present AND resolve to a row named canonical. A
	// missing id is the reported root cause (silent built-in default fallback);
	// a dangling id (row deleted/renamed) is the harder case the resolver catches.
	id := a.GetTLSFingerprintProfileID()
	if id <= 0 {
		return "tls_profile_unbound"
	}
	if r.tlsProfiles != nil {
		if prof, err := r.tlsProfiles.GetByID(ctx, id); err == nil {
			if prof == nil || prof.Name != eff.TLSProfileName {
				return "tls_profile_dangling"
			}
		}
		// err != nil → skip this sub-check (conservative; don't flap on read error)
	}
	// credentials self-protection template keys must be present (presence, not
	// value-equality — avoids fighting benign operator edits).
	for k := range eff.Credentials {
		if _, ok := a.Credentials[k]; !ok {
			return "credentials_template_missing"
		}
	}
	return ""
}
