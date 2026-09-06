package service

import (
	"context"
	"time"
)

// withWindowCostPrefetch is a TK seam for optional window-cost prefetch before
// candidate filtering. Currently a pass-through; kept so call sites stay stable.
func (s *GatewayService) withWindowCostPrefetch(ctx context.Context, accounts []Account) context.Context {
	return ctx
}

// isAccountSchedulableForWindowCost gates Anthropic OAuth/setup-token accounts
// via upstream 5h/7d passive utilization (see anthropic_account_scheduler_tk_window_sched.go).
func (s *GatewayService) isAccountSchedulableForWindowCost(ctx context.Context, account *Account, isSticky bool) bool {
	return s.isAccountSchedulableForAnthropicWindow(ctx, account, isSticky)
}

// tkAllowOrCollectWindowCost applies the non-sticky window-cost gate. On reject,
// the account is recorded for never-empty-pool recovery via
// tkRecoverAnthropicCandidatesFromWindowDropped.
func (s *GatewayService) tkAllowOrCollectWindowCost(ctx context.Context, acc *Account, windowDropped *[]*Account) bool {
	if s.isAccountSchedulableForWindowCost(ctx, acc, false) {
		return true
	}
	*windowDropped = append(*windowDropped, acc)
	return false
}

// tkRecoverAnthropicCandidatesFromWindowDropped restores the least-utilized
// Anthropic account from the window-cost drop list when Layer-2 load-balance
// has no other candidates (never-empty-pool).
func tkRecoverAnthropicCandidatesFromWindowDropped(candidates, windowDropped []*Account, now time.Time) []*Account {
	if len(candidates) > 0 || len(windowDropped) == 0 {
		return candidates
	}
	if acc := leastUtilizedAnthropicAccount(windowDropped, now); acc != nil {
		candidates = append(candidates, acc)
	}
	return candidates
}
