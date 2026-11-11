package service

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// Upstream #8393: every non-image Gemini model cooldown is also written to and
// read from the shared antigravity:gemini family key, so an exhausted Argon on
// a Pro account also blocks Flash on that account.
//
// Evidence that the two quotas are separate on Pro only:
//   - issue #8393: Pro Argon quota is separate; Ultra Argon shares the Flash pool.
//   - upstream 455751402: Pro accounts report a third quota group
//     gemini-argon-wtu ("For paid Pro plans, Argon quota is separate").
//   - docs/approved/antigravity-quota-scope.md: the 2026-10-30 prod snapshot of
//     two PRO accounts shows different Argon and Flash reset windows. There is
//     no ULTRA sample.
//
// So only accounts whose plan_type normalizes to Pro split Argon into its own
// sub-family. Every other tier, and accounts with no plan_type, keep the
// shared family key unchanged. All Argon variants (low/medium/high) share the
// sub-family so one exhausted variant cannot be bypassed through another.
const antigravityGeminiArgonRateLimitKey = "antigravity:gemini:argon"

func isAntigravityGeminiArgonModel(model string) bool {
	normalized := normalizeAntigravityModelName(model)
	return strings.HasPrefix(normalized, "gemini-") && strings.Contains(normalized, "-argon")
}

// tkAntigravityArgonQuotaSeparate reports whether this account's Argon quota
// is separate from the shared Gemini pool. plan_type comes from LoadCodeAssist
// and is refreshed on every token refresh; it stays in the scheduler snapshot
// (filterSchedulerCredentials keeps it).
func tkAntigravityArgonQuotaSeparate(account *Account) bool {
	if account == nil {
		return false
	}
	plan := strings.TrimSpace(account.GetCredential("plan_type"))
	if plan == "" {
		plan = strings.TrimSpace(account.GetExtraString("plan_type"))
	}
	return strings.EqualFold(antigravity.TierIDToPlanType(plan), "Pro")
}

// tkAntigravityModelRateLimitWriteKeys returns the scopes to write for a
// model cooldown on this account. Only a Pro account's Argon model differs
// from antigravityModelRateLimitKeys.
func tkAntigravityModelRateLimitWriteKeys(account *Account, model string) []string {
	model = strings.TrimSpace(model)
	if model != "" && isAntigravityGeminiArgonModel(model) && tkAntigravityArgonQuotaSeparate(account) {
		return []string{model, antigravityGeminiArgonRateLimitKey}
	}
	return antigravityModelRateLimitKeys(account, model)
}

// tkAntigravityGeminiFamilyReadKeys returns the family scopes a non-image
// Gemini request must check on this account, in addition to its exact model.
//
// A non-Pro account also reads the Argon sub-family, so an Argon cooldown
// written while the account was Pro still blocks the shared pool after a tier
// change, until it expires.
func tkAntigravityGeminiFamilyReadKeys(account *Account, model string) []string {
	if tkAntigravityArgonQuotaSeparate(account) {
		if isAntigravityGeminiArgonModel(model) {
			return []string{antigravityGeminiArgonRateLimitKey}
		}
		return []string{antigravityGeminiModelRateLimitKey}
	}
	return []string{antigravityGeminiModelRateLimitKey, antigravityGeminiArgonRateLimitKey}
}
