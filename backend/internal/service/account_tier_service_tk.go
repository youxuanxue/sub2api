package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/model"
)

// AccountTierExtraKey is the TK-owned extra key recording which tier was applied
// to an account. It is retained as a human-readable label / migration aid; the
// authoritative binding is now account.tier_id. The config reconciler's
// tier-drift report and the migration backfill key off it.
const AccountTierExtraKey = "stability_tier"

// AccountTierService binds a single anthropic OAUTH account to a stability tier
// on THIS deployment's database. In the reference-table model "apply tier" =
//   - set account.tier_id (per-tier config — base_rpm / max_sessions / window /
//     ... — is resolved at runtime from the tiers table, NOT copied here);
//   - value-sync account.concurrency from the tier row (concurrency stays a
//     persisted column on the scheduler hot path; see reconciler Step T for the
//     steady-state re-assertion);
//   - persist the canonical TLS fingerprint binding (stable; profile CONTENT
//     fans out via the TLS pub/sub, this only pins which profile id);
//   - it NEVER overwrites the shared TLS profile content (fixes the old冲刷) and
//     NEVER copies rpm/sessions/window into the account's extra.
//
// It deliberately does NOT touch priority — accounts.priority is owned by the
// window-rebalance pipeline. Fleet fan-out stays with the ops/anthropic pipeline.
type AccountTierService struct {
	adminSvc AdminService
	tierSvc  *TierService
	tlsSvc   *TLSFingerprintProfileService
}

// NewAccountTierService wires the tier-apply service.
func NewAccountTierService(adminSvc AdminService, tierSvc *TierService, tlsSvc *TLSFingerprintProfileService) *AccountTierService {
	return &AccountTierService{adminSvc: adminSvc, tierSvc: tierSvc, tlsSvc: tlsSvc}
}

// ApplyTier binds the given account to `tier`. Only anthropic OAUTH accounts are
// accepted (apikey mirror stubs and other platforms are rejected — applying a
// tier to a stub would wipe its base_url/pool_mode).
func (s *AccountTierService) ApplyTier(ctx context.Context, accountID int64, tier string) (*Account, error) {
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
	if account.Type != AccountTypeOAuth {
		return nil, fmt.Errorf("apply-tier is only supported for anthropic OAuth accounts (account %d is type %q); tier does not apply to api-key / mirror-stub accounts", accountID, account.Type)
	}

	row, err := s.tierSvc.GetByName(ctx, tier)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("unknown tier %q", tier)
	}

	// Resolve the canonical TLS fingerprint profile id for the binding (stable;
	// the profile CONTENT is owned by the pipeline / TLS pub/sub, we only pin
	// which id). Prefer the tier row's explicit id; else resolve by name. A
	// missing id would make the runtime fall back to the built-in default — but
	// that is non-fatal for the binding, so we only warn.
	var tlsProfileID int64
	if row.TLSProfileID != nil && *row.TLSProfileID > 0 {
		tlsProfileID = *row.TLSProfileID
	} else if row.TLSProfileName != nil && *row.TLSProfileName != "" && s.tlsSvc != nil {
		if p, err := s.tlsSvc.GetByName(ctx, *row.TLSProfileName); err == nil && p != nil {
			tlsProfileID = p.ID
		}
	}

	// Build the desired extra: preserve the account's existing (non-tier-managed)
	// extra, pin the TLS binding + tier label. The tier-managed numeric keys
	// (base_rpm / ...) are intentionally NOT written — they are resolved at
	// runtime from the tier row, and repo.Update strips any overlaid copies.
	extra := make(map[string]any, len(account.Extra)+3)
	for k, v := range account.Extra {
		if model.IsTierManagedExtraKey(k) {
			continue
		}
		extra[k] = v
	}
	extra["enable_tls_fingerprint"] = true
	if tlsProfileID > 0 {
		extra["tls_fingerprint_profile_id"] = tlsProfileID
	}
	extra[AccountTierExtraKey] = row.Name

	tierID := row.ID
	concurrency := row.Concurrency
	rateMultiplier := row.RateMultiplier

	input := &UpdateAccountInput{
		TierID:         &tierID,
		Concurrency:    &concurrency, // value-sync from tier (hot-path column)
		RateMultiplier: &rateMultiplier,
		Extra:          extra,
		// Priority intentionally omitted — owned by the window-rebalance pipeline.
	}

	updated, err := s.adminSvc.UpdateAccount(ctx, accountID, input)
	if err != nil {
		return nil, err
	}

	slog.Info("account tier bound (local deployment only)",
		"account_id", accountID,
		"account_name", account.Name,
		"tier", row.Name,
		"tier_id", tierID,
		"concurrency", concurrency,
		"tls_fingerprint_profile_id", tlsProfileID,
	)
	return updated, nil
}
