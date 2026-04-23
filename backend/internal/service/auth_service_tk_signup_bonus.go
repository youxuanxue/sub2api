package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// TokenKey: new-user signup bonus is isolated from auth_service.go to keep
// upstream merges small.
//
// Wiring (US-029):
//   - applySignupBonusUSD     ← invoked at INSERT-time inside each register path
//   - logSignupBonusCredited  ← invoked best-effort after a successful Create
//
// Defaults and admin overrides live in setting_service_tk_cold_start.go
// (key = signup_bonus_enabled / signup_bonus_balance, default $1.00 USD).
//
// Architecture notes (see docs/approved/user-cold-start.md §3 for full rationale):
//   - Bonus is baked into the User.Balance field at INSERT time → atomic with
//     user creation by virtue of being a single SQL statement (no extra Tx).
//   - Audit log is best-effort (mirrors PromoService failure log pattern):
//     write failure does NOT roll back the registration. The product invariant
//     "user exists ↔ user.balance includes bonus" still holds because the bonus
//     is part of the same INSERT row.
//   - "Source" tags differentiate paths in logs ("email" / "oauth") so admins
//     can distinguish signup channels without parsing the message string.

// signupBonusSource enumerates the call site for audit purposes. Keep these
// values stable — they are part of the structured log contract and parsed by
// any future operator dashboard.
const (
	signupBonusSourceEmail = "email"
	signupBonusSourceOAuth = "oauth"
)

// tkApplyColdStartPostCreate is the single call-site shim invoked from each
// register path right after a fresh user row is committed. It encapsulates
// the two cold-start side effects (audit log + trial-key issuance) so the
// upstream-shaped auth_service.go only carries ONE TK line per register path
// instead of two — minimizes merge friction (CLAUDE.md §5).
//
// Callers must guarantee:
//   - `userID` belongs to a row that was actually inserted (not an existing
//     user from an email-conflict race).
//   - `bonusUSD` is the value previously returned by applySignupBonusUSD for
//     the same user so the audit log reflects the credited amount.
//
// Both downstream operations are best-effort and never error out — they
// MUST NOT make a successful registration look like a failure to the user.
func (s *AuthService) tkApplyColdStartPostCreate(ctx context.Context, userID int64, bonusUSD float64, source string) {
	if s == nil {
		return
	}
	s.logSignupBonusCredited(userID, bonusUSD, source)
	s.issueTrialKeyIfEnabled(ctx, userID)
}

// applySignupBonusUSD reads the configured signup bonus and returns
// (totalBalance, bonusAmount). The caller writes totalBalance into User.Balance
// at INSERT and then logs bonusAmount post-Create when > 0.
//
// Returns (baseBalance, 0) when settingService is nil or the bonus is disabled
// — in that case the caller skips the audit log entirely (no noise).
func (s *AuthService) applySignupBonusUSD(ctx context.Context, baseBalance float64) (total float64, bonus float64) {
	if s == nil || s.settingService == nil {
		return baseBalance, 0
	}
	bonus = s.settingService.ComputeSignupBonus(ctx)
	if bonus < 0 {
		// ComputeSignupBonus already clamps negatives to 0, but defend twice
		// because admin-typed values flow through here.
		bonus = 0
	}
	return baseBalance + bonus, bonus
}

// logSignupBonusCredited writes a best-effort structured audit line.
// Format is fixed: parsers can split on space-separated key=value tokens.
// userID and amount go through structured fields (not %s formatting) so a
// crafted username can never inject log lines (US-029 Risk Focus / 安全问题).
func (s *AuthService) logSignupBonusCredited(userID int64, bonusUSD float64, source string) {
	if bonusUSD <= 0 {
		return
	}
	logger.LegacyPrintf(
		"service.auth",
		"[Auth] signup_bonus_credited userID=%d amount_usd=%.2f source=%s",
		userID, bonusUSD, source,
	)
}
