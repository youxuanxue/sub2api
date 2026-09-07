package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// gatewayAccountEligible is the common hard gate for sticky, routed, legacy
// and load-aware selection. Window recovery and slot acquisition are separate.
func (s *GatewayService) gatewayAccountEligible(ctx context.Context, account *Account, platform string, mixed bool, model string, sticky bool) bool {
	return s.gatewayAccountEligibilityReason(ctx, account, platform, mixed, model, sticky) == ""
}

func (s *GatewayService) gatewayAccountEligibilityReason(ctx context.Context, account *Account, platform string, mixed bool, model string, sticky bool) string {
	switch {
	case !s.isAccountSchedulableForSelection(account):
		return "not_schedulable"
	case !s.isGatewayAccountProfitEligible(ctx, account):
		return "profit_ineligible"
	case !s.isAccountAllowedForPlatformModel(ctx, account, platform, mixed, model):
		return "platform_mismatch"
	case model != "" && !s.isModelSupportedByAccountWithContext(ctx, account, model):
		return "model_unsupported"
	case !s.isAccountSchedulableForModelSelection(ctx, account, model):
		return "model_cooling"
	case !s.isAccountSchedulableForQuota(account):
		return "quota_blocked"
	case !s.isAccountSchedulableForRPM(ctx, account, sticky):
		return "rpm_red"
	}
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && group != nil {
		if group.RequirePrivacySet && !account.IsPrivacySet() {
			return "privacy_not_set"
		}
		if s.needsUpstreamChannelRestrictionCheck(ctx, &group.ID) &&
			s.isUpstreamModelRestrictedByChannel(ctx, group.ID, account, model) {
			return "channel_upstream_restricted"
		}
	}
	return ""
}

// gatewayCandidates owns non-sticky candidate gates, including never-empty
// window recovery. Both direct scheduling and Universal pre-billing use it.
// Slot acquisition and session registration remain in the selector.
func (s *GatewayService) gatewayCandidates(ctx context.Context, accounts []Account, platform string, useMixed bool, requestedModel string, excludedIDs map[int64]struct{}) []*Account {
	candidates := make([]*Account, 0, len(accounts))
	var windowDropped []*Account
	for i := range accounts {
		acc := &accounts[i]
		if _, excluded := excludedIDs[acc.ID]; excluded {
			continue
		}
		if !s.gatewayAccountEligible(ctx, acc, platform, useMixed, requestedModel, false) {
			continue
		}
		if !s.tkAllowOrCollectWindowCost(ctx, acc, &windowDropped) {
			continue
		}
		candidates = append(candidates, acc)
	}
	candidates = tkRecoverAnthropicCandidatesFromWindowDropped(candidates, windowDropped, time.Now())

	return candidates
}
