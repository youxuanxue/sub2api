package repository

import "github.com/Wei-Shaw/sub2api/internal/service"

// tkExtraToPersist returns Extra suitable for DB write: tier-overlaid runtime
// keys are stripped so they never leak to persistence ("零账号写").
func (r *accountRepository) tkExtraToPersist(account *service.Account) map[string]any {
	extraToPersist := account.Extra
	if r.tierResolver != nil {
		extraToPersist = r.tierResolver.TierManagedExtraStripped(account)
	}
	return extraToPersist
}

// tkApplyTierExtraOnLoad overlays per-tier config onto the in-memory Extra so
// runtime getters resolve tier values; nil-safe.
func (r *accountRepository) tkApplyTierExtraOnLoad(out *service.Account) {
	if r.tierResolver != nil {
		r.tierResolver.ApplyTierExtra(out)
	}
}
