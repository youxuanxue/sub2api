package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/baseline"
	"github.com/Wei-Shaw/sub2api/internal/model"
)

// AccountTierExtraKey is the TK-owned extra key recording which tier was applied
// to an account. It is retained as a human-readable label / migration aid; the
// authoritative binding is now account.tier_id. The config reconciler's
// tier-drift report and the migration backfill key off it.
const AccountTierExtraKey = "stability_tier"

// AccountTierService binds a single anthropic OAUTH / setup-token account to a
// stability tier on THIS deployment's database AND materializes the full
// shared_baseline so "apply tier" yields a COMPLETE account in one shot. In the
// reference-table model "apply tier" =
//   - set account.tier_id (per-tier NUMERIC config — base_rpm / max_sessions /
//     window / ... — is resolved at runtime from the tiers table, NOT copied here);
//   - value-sync account.concurrency from the tier row (concurrency stays a
//     persisted column on the scheduler hot path; see reconciler Step T for the
//     steady-state re-assertion);
//   - ensure the canonical TLS fingerprint profile ROW EXISTS (GetOrUpsertByName
//     from the embedded shared_baseline) and pin account.extra.tls_fingerprint_
//     profile_id to it. This fixes the historical root cause: the old path only
//     *looked up* the id (GetByName) and silently left the binding empty when the
//     row was missing, so the account ran on the built-in default ClientHello;
//   - write the shared_baseline credentials self-protection template
//     (temp_unschedulable_enabled / temp_unschedulable_rules /
//     intercept_warmup_requests) — OAuth tokens are preserved by
//     MergePreservingSensitiveCreds in UpdateAccount;
//   - write the shared_baseline extra mimicry/template flags (enable_tls_
//     fingerprint / rpm_strategy / session_id_masking_enabled / ...);
//   - (operator-explicit ApplyTier ONLY) seed priority from the tier baseline.
//     priority is a DYNAMIC runtime signal owned by the window-rebalance pipeline
//     (ops/anthropic/rebalance-anthropic-priority.py): git baseline wins only at
//     the apply/发版 moment, and the reconciler self-heal path
//     (ReapplyBaselineInfra) deliberately does NOT touch priority — reverting it
//     every tick would flatten the window-aware ordering rebalance just computed.
//     Contrast kiro, which has no rebalance pipeline and is hard-pinned by
//     reconcileKiroPriorityBaseline.
//
// It NEVER copies the tier-managed NUMERIC extra keys (base_rpm / max_sessions /
// window / cache_ttl_override_*) into the account — those overlay at runtime from
// the tiers table (model.IsTierManagedExtraKey strips them). Fleet fan-out stays
// with the ops/anthropic pipeline; the per-node reconciler re-asserts this same
// complete shape via ReapplyBaselineInfra (everything EXCEPT priority) so any
// creation path self-heals without flattening the window-rebalance ordering.
type AccountTierService struct {
	adminSvc AdminService
	tierSvc  *TierService
	tlsSvc   *TLSFingerprintProfileService
}

// NewAccountTierService wires the tier-apply service.
func NewAccountTierService(adminSvc AdminService, tierSvc *TierService, tlsSvc *TLSFingerprintProfileService) *AccountTierService {
	return &AccountTierService{adminSvc: adminSvc, tierSvc: tierSvc, tlsSvc: tlsSvc}
}

// ApplyTier is the operator-explicit path (admin UI "apply tier" / deploy seed).
// It materializes the full shared_baseline AND seeds priority from the tier
// baseline — this is the single git-is-source-of-truth moment for priority. Use
// it when an operator deliberately (re)applies a tier; between applies the
// window-rebalance pipeline owns runtime priority.
func (s *AccountTierService) ApplyTier(ctx context.Context, accountID int64, tier string) (*Account, error) {
	return s.applyTier(ctx, accountID, tier, true /* syncPriority */)
}

// ReapplyBaselineInfra is the reconciler self-heal path. It re-asserts the
// shared_baseline INFRASTRUCTURE (TLS profile ensure+bind, credentials
// self-protection template, extra mimicry flags, concurrency) but DELIBERATELY
// does NOT touch priority: priority is a dynamic runtime signal owned by the
// window-rebalance pipeline, and value-syncing it here on every tick would revert
// rebalance's window-aware ordering back to the uniform baseline. Steady-state
// self-heal must converge the infra without fighting the rebalance pipeline.
func (s *AccountTierService) ReapplyBaselineInfra(ctx context.Context, accountID int64, tier string) (*Account, error) {
	return s.applyTier(ctx, accountID, tier, false /* syncPriority */)
}

// applyTier binds the given account to `tier`. Only anthropic OAUTH and
// setup-token accounts are accepted — these are the two types subject to 5h
// window + session control (see Account.IsAnthropicOAuthOrSetupToken). apikey
// mirror stubs and other platforms are rejected — applying a tier to a stub
// would wipe its base_url/pool_mode. syncPriority gates whether priority is
// value-synced to the tier baseline (true on the operator path, false on the
// reconciler self-heal path); see ApplyTier / ReapplyBaselineInfra.
func (s *AccountTierService) applyTier(ctx context.Context, accountID int64, tier string, syncPriority bool) (*Account, error) {
	if s == nil || s.adminSvc == nil || s.tierSvc == nil {
		return nil, fmt.Errorf("account tier service unavailable")
	}

	account, err := s.adminSvc.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}
	if account.Platform != PlatformAnthropic {
		return nil, fmt.Errorf("apply-tier is only supported for anthropic accounts (account %d is platform %q)", accountID, account.Platform)
	}
	if account.Type != AccountTypeOAuth && account.Type != AccountTypeSetupToken {
		return nil, fmt.Errorf("apply-tier is only supported for anthropic OAuth / setup-token accounts (account %d is type %q); tier does not apply to api-key / mirror-stub accounts", accountID, account.Type)
	}

	row, err := s.tierSvc.GetByName(ctx, tier)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("unknown tier %q", tier)
	}

	// Load the merged shared_baseline for this tier (TLS spec + credentials +
	// extra template + priority). The embedded baseline JSON is the single source
	// of truth; the Python fleet pipeline upserts the same content.
	eff, err := baseline.EffectiveBaselineForTier(row.Name)
	if err != nil {
		return nil, fmt.Errorf("load tier baseline %q: %w", row.Name, err)
	}

	// Ensure the canonical TLS profile ROW EXISTS (upsert by name) and bind its id.
	// GetOrUpsertByName creates-if-absent and returns the row id — this is the fix
	// for "tls_fingerprint_profiles has no tk_canonical_cc_oauth" → silent fallback
	// to the built-in default ClientHello. A missing TLS service or a failed upsert
	// is now FATAL (return error): an unbound canonical profile is exactly the bug
	// we are eliminating, so it must not pass silently.
	if s.tlsSvc == nil {
		return nil, fmt.Errorf("tls fingerprint service unavailable; cannot ensure canonical profile %q", eff.TLSProfileName)
	}
	prof, err := s.tlsSvc.GetOrUpsertByName(ctx, eff.CanonicalTLSProfile())
	if err != nil {
		return nil, fmt.Errorf("ensure canonical TLS profile %q: %w", eff.TLSProfileName, err)
	}
	tlsProfileID := prof.ID

	// desired extra = account's own non-tier-managed extra, overlaid with the
	// shared_baseline template flags (enable_tls_fingerprint / rpm_strategy /
	// session_id_masking_enabled / ...), plus the TLS binding + tier label. The
	// tier-managed NUMERIC keys (base_rpm / max_sessions / window / cache_ttl_*)
	// are stripped from BOTH sides — they overlay at runtime from the tiers table.
	extra := make(map[string]any, len(account.Extra)+len(eff.Extra)+2)
	for k, v := range account.Extra {
		if model.IsTierManagedExtraKey(k) {
			continue
		}
		extra[k] = v
	}
	for k, v := range eff.Extra {
		if model.IsTierManagedExtraKey(k) {
			continue
		}
		extra[k] = v
	}
	extra["tls_fingerprint_profile_id"] = tlsProfileID
	extra[AccountTierExtraKey] = row.Name

	// desired credentials = the account's existing (full, unredacted — GetAccount →
	// repo.GetByID returns real tokens at the service layer) credentials overlaid
	// with the shared_baseline credentials template (temp_unschedulable_* +
	// intercept_warmup_requests). UpdateAccount runs MergePreservingSensitiveCreds,
	// so OAuth tokens are doubly safe; copying existing creds through also avoids
	// dropping any non-sensitive metadata.
	creds := make(map[string]any, len(account.Credentials)+len(eff.Credentials))
	for k, v := range account.Credentials {
		creds[k] = v
	}
	for k, v := range eff.Credentials {
		creds[k] = v
	}

	tierID := row.ID
	concurrency := row.Concurrency
	rateMultiplier := row.RateMultiplier

	input := &UpdateAccountInput{
		TierID:         &tierID,
		Concurrency:    &concurrency, // value-sync from tier (hot-path column)
		RateMultiplier: &rateMultiplier,
		Credentials:    creds,
		Extra:          extra,
	}
	// priority is seeded from the tier baseline ONLY on the operator-explicit
	// path (syncPriority). The reconciler self-heal path leaves priority untouched
	// so the window-rebalance pipeline's runtime ordering is not flattened.
	if syncPriority {
		priority := eff.Priority
		input.Priority = &priority
	}

	updated, err := s.adminSvc.UpdateAccount(ctx, accountID, input)
	if err != nil {
		return nil, err
	}

	slog.Info("account tier applied — baseline materialized (local deployment only)",
		"account_id", accountID,
		"account_name", account.Name,
		"tier", row.Name,
		"tier_id", tierID,
		"concurrency", concurrency,
		"priority_synced", syncPriority,
		"tls_fingerprint_profile_id", tlsProfileID,
	)
	return updated, nil
}
