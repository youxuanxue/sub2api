package service

import (
	"context"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

// resolveGroupPlatform returns the canonical Group.Platform value for a given
// groupID. It is the single point where the OpenAI-compat gateway answers
// "is this a newapi group, an openai group, or schedule-everything (no group)?".
//
// Behavior (design `docs/approved/newapi-as-fifth-platform.md` §3.2):
//   - groupID == nil OR snapshot/repo unavailable OR group not found → returns
//     PlatformOpenAI. This preserves the legacy hardcoded behavior for the
//     "no group / RunModeSimple / startup race" cases — the cheapest defense
//     against ever sending a request through with an empty groupPlatform.
//   - groupID points at a real Group → returns g.Platform verbatim
//     (typically "openai", "newapi", or other compat platforms added later).
//
// resolveGroupPlatform is the OPC-style funnel: every plumbing point that needs
// "what platform is this scheduling pool?" reads the same answer here, so a
// future sixth compat platform only requires updating OpenAICompatPlatforms()
// and isOpenAICompatPlatformGroup() — never the call sites.
func (s *OpenAIGatewayService) resolveGroupPlatform(ctx context.Context, groupID *int64) string {
	if s == nil || groupID == nil || *groupID == 0 {
		return PlatformOpenAI
	}
	if s.schedulerSnapshot == nil {
		return PlatformOpenAI
	}
	g, err := s.schedulerSnapshot.GetGroupByID(ctx, *groupID)
	if err != nil || g == nil || g.Platform == "" {
		return PlatformOpenAI
	}
	return g.Platform
}

// listOpenAICompatSchedulableAccounts is the multi-platform-aware replacement
// for listSchedulableAccounts. It pushes groupPlatform down into the scheduler
// snapshot bucket key (and the legacy direct-DB fallback), so a newapi group
// gets its own bucket and never sees openai accounts (and vice versa).
//
// Design §2.2 / §2.4: the bucket key is naturally partitioned by platform,
// so existing openai buckets stay warm after upgrade and a newapi bucket
// cold-starts on first request — no cache invalidation needed.
func (s *OpenAIGatewayService) listOpenAICompatSchedulableAccounts(ctx context.Context, groupID *int64, groupPlatform string) ([]Account, error) {
	if groupPlatform == "" {
		groupPlatform = PlatformOpenAI
	}
	if s.schedulerSnapshot != nil {
		accounts, _, err := s.schedulerSnapshot.ListSchedulableAccounts(ctx, groupID, groupPlatform, false)
		return accounts, err
	}
	var accounts []Account
	var err error
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		accounts, err = s.accountRepo.ListSchedulableByPlatform(ctx, groupPlatform)
	} else if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, *groupID, groupPlatform)
	} else {
		accounts, err = s.accountRepo.ListSchedulableUngroupedByPlatform(ctx, groupPlatform)
	}
	if err != nil {
		return nil, err
	}
	return accounts, nil
}

// listSchedulableAccounts is the legacy entrypoint preserved for callers that
// have not (yet) been threaded with groupPlatform. New code paths SHOULD call
// listOpenAICompatSchedulableAccounts directly with the resolved platform —
// see docs/approved/newapi-as-fifth-platform.md §3.1 U1 / §3.2.
func (s *OpenAIGatewayService) listSchedulableAccounts(ctx context.Context, groupID *int64, groupPlatform string) ([]Account, error) {
	accounts, err := s.listOpenAICompatSchedulableAccounts(ctx, groupID, groupPlatform)
	if err != nil {
		return nil, fmt.Errorf("query accounts failed: %w", err)
	}
	return accounts, nil
}

// resolveFreshSchedulableOpenAIAccount re-validates a candidate account against
// the OpenAI-compatible scheduling pool of the given groupPlatform. Empty
// groupPlatform falls back to PlatformOpenAI for backward compatibility.
// See docs/approved/newapi-as-fifth-platform.md §3.1 U6.
func (s *OpenAIGatewayService) resolveFreshSchedulableOpenAIAccount(ctx context.Context, account *Account, requestedModel string, groupPlatform string) *Account {
	if account == nil {
		return nil
	}
	if groupPlatform == "" {
		groupPlatform = PlatformOpenAI
	}

	fresh := account
	if s.schedulerSnapshot != nil {
		current, err := s.getSchedulableAccount(ctx, account.ID)
		if err != nil || current == nil {
			return nil
		}
		fresh = current
	}

	if !fresh.IsSchedulable() || !fresh.IsOpenAICompatPoolMember(groupPlatform) {
		return nil
	}
	if requestedModel != "" && !fresh.IsModelSupported(requestedModel) {
		return nil
	}
	return fresh
}

// recheckSelectedOpenAIAccountFromDB re-reads the account from PG and validates
// it against the OpenAI-compatible scheduling pool of groupPlatform. Empty
// groupPlatform falls back to PlatformOpenAI for backward compatibility.
// See docs/approved/newapi-as-fifth-platform.md §3.1 U6 (extension: design
// originally only listed resolveFresh, but recheck performs the symmetric
// IsOpenAI() filter and was a design oversight — both must move together).
func (s *OpenAIGatewayService) recheckSelectedOpenAIAccountFromDB(ctx context.Context, account *Account, requestedModel string, groupPlatform string) *Account {
	if account == nil {
		return nil
	}
	if s.schedulerSnapshot == nil || s.accountRepo == nil {
		return account
	}
	if groupPlatform == "" {
		groupPlatform = PlatformOpenAI
	}

	latest, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || latest == nil {
		return nil
	}
	if !latest.IsSchedulable() || !latest.IsOpenAICompatPoolMember(groupPlatform) {
		return nil
	}
	if requestedModel != "" && !latest.IsModelSupported(requestedModel) {
		return nil
	}
	return latest
}
