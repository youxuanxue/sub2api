package service

import (
	"context"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Sticky affinity and account cooling must never disagree.
//
// A session binding is a best-effort preference: it asks the scheduler to reuse
// the same upstream account so the provider can reuse prefix KV cache. The
// moment that account is cooled for the requested model, keeping the binding is
// strictly harmful — the scheduler refuses the pinned account, the caller learns
// nothing, and every retry replays into the same wall until the (one hour)
// binding expires. That is the amplification this file exists to prevent.
//
// The repair is deliberately two-sided and asymmetric:
//
//   - tkDropStickyForUnusableAccount removes a binding that points at an account
//     which has just become unusable. It runs on the cooling path, before the
//     next selection, so the retry inside the SAME request is already free.
//   - tkRebindStickyAfterFailover re-pins the session onto the account that
//     actually served the request. This is what keeps the fix cheap: without it
//     a dropped binding would leave the session affinity-less, re-selecting (and
//     likely missing the prefix cache) on every subsequent turn. With it, at most
//     one turn pays for the migration and the session is stable again.
//
// Nothing here runs on the healthy path. A request that never cools an account
// never touches the cache, so steady-state cache behaviour is unchanged.

// tkDropStickyForUnusableAccount drops the session binding when it currently
// points at the given account. Clearing is conditional on an actual match on
// purpose: a concurrent turn may have already re-bound the session to a healthy
// account, and blindly deleting would throw that healthy affinity away.
//
// Both nil/empty inputs and non-matching bindings are silent no-ops so callers
// can invoke this from shared error paths without extra branching.
func (s *AntigravityGatewayService) tkDropStickyForUnusableAccount(
	ctx context.Context,
	groupID int64,
	sessionHash string,
	account *Account,
) {
	if s == nil || s.cache == nil || sessionHash == "" || account == nil || account.ID <= 0 {
		return
	}
	if !s.tkStickyBoundTo(ctx, groupID, sessionHash, account.ID) {
		return
	}
	if err := s.cache.DeleteSessionAccountID(ctx, groupID, sessionHash); err != nil {
		logger.LegacyPrintf("service.antigravity_gateway",
			"[Sticky] drop_for_cooled_account_failed session=%s account_id=%d error=%v",
			shortSessionHash(sessionHash), account.ID, err)
		return
	}
	logger.LegacyPrintf("service.antigravity_gateway",
		"[Sticky] dropped_for_cooled_account session=%s account_id=%d",
		shortSessionHash(sessionHash), account.ID)
}

// tkRebindStickyAfterFailover re-pins the session to the account that served the
// request after a failover switch, so prefix-cache reuse resumes immediately
// instead of degrading into per-turn re-selection.
//
// previousAccountID is the account the session was bound to before the switch
// (0 when there was no binding). Rebinding is skipped when the session already
// points at the serving account, which makes repeated calls cheap and idempotent.
func (s *AntigravityGatewayService) tkRebindStickyAfterFailover(
	ctx context.Context,
	groupID int64,
	sessionHash string,
	previousAccountID int64,
	servingAccount *Account,
) {
	if s == nil || s.cache == nil || sessionHash == "" || servingAccount == nil || servingAccount.ID <= 0 {
		return
	}
	if previousAccountID == servingAccount.ID {
		return
	}
	// Already migrated (possibly by a concurrent turn): nothing to do. Checked
	// before the ownership guard below so an idempotent repeat is a cheap no-op.
	if bound, ok := s.tkStickyAccountID(ctx, groupID, sessionHash); ok && bound == servingAccount.ID {
		return
	}
	// The drop path normally cleared this, but a concurrent turn may have written
	// a fresh binding while this request was in flight. Only overwrite when the
	// binding still points at the account we failed over from, or is absent.
	if bound, ok := s.tkStickyAccountID(ctx, groupID, sessionHash); ok && bound != previousAccountID {
		return
	}
	if err := s.cache.SetSessionAccountID(ctx, groupID, sessionHash, servingAccount.ID, stickySessionTTL); err != nil {
		logger.LegacyPrintf("service.antigravity_gateway",
			"[Sticky] rebind_after_failover_failed session=%s account_id=%d error=%v",
			shortSessionHash(sessionHash), servingAccount.ID, err)
		return
	}
	logger.LegacyPrintf("service.antigravity_gateway",
		"[Sticky] rebound_after_failover session=%s from_account_id=%d to_account_id=%d",
		shortSessionHash(sessionHash), previousAccountID, servingAccount.ID)
}

// tkStickyAccountID reads the current binding, reporting whether one exists.
// A read error is reported as "no binding" so callers fall back to the safe
// repair action rather than skipping it.
func (s *AntigravityGatewayService) tkStickyAccountID(
	ctx context.Context,
	groupID int64,
	sessionHash string,
) (int64, bool) {
	if s == nil || s.cache == nil || sessionHash == "" {
		return 0, false
	}
	accountID, err := s.cache.GetSessionAccountID(ctx, groupID, sessionHash)
	if err != nil || accountID <= 0 {
		return 0, false
	}
	return accountID, true
}

// tkStickyBoundTo reports whether the session is currently pinned to accountID.
func (s *AntigravityGatewayService) tkStickyBoundTo(
	ctx context.Context,
	groupID int64,
	sessionHash string,
	accountID int64,
) bool {
	bound, ok := s.tkStickyAccountID(ctx, groupID, sessionHash)
	return ok && bound == accountID
}
