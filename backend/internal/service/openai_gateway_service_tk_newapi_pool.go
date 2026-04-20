package service

import (
	"context"

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
