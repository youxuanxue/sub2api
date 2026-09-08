package service

import (
	"context"
	"time"
)

// openAICandidates is the shared candidate filter; scoring and acquisition stay
// in the OpenAI selector, while Universal only observes its result.
func (s *defaultOpenAIAccountScheduler) openAICandidates(ctx context.Context, accounts []Account, req OpenAIAccountScheduleRequest) ([]*Account, openAISelectionFilterStats) {
	eligible, stats := s.openAICandidatesBeforeWindow(ctx, accounts, req)
	filtered := make([]*Account, 0, len(eligible))
	var dropped []*Account
	for _, account := range eligible {
		if !s.service.isAccountSchedulableForOpenAIWindow(ctx, account, false) {
			dropped = append(dropped, account)
		} else {
			filtered = append(filtered, account)
		}
	}
	if len(filtered) == 0 && len(dropped) > 0 {
		if account := leastUtilizedOpenAIAccount(dropped, time.Now()); account != nil {
			filtered = append(filtered, account)
		}
	}
	return filtered, stats
}

// The global candidate owner applies window recovery after union and payment
// admission; the legacy selector applies it to its own pool above.
func (s *defaultOpenAIAccountScheduler) openAICandidatesBeforeWindow(ctx context.Context, accounts []Account, req OpenAIAccountScheduleRequest) ([]*Account, openAISelectionFilterStats) {
	poolPlatform := req.schedulePlatform()
	accounts = s.filterGrokFreeQuotaAccounts(ctx, accounts)
	if poolPlatform == PlatformGrok {
		accounts = filterGrokTeamModelRateLimitedAccounts(accounts, req.RequestedModel, time.Now())
		accounts = filterGrokModelQuotaBlockedAccounts(accounts, req.RequestedModel, time.Now())
	}
	// require_privacy_set: 获取分组信息
	var schedGroup *Group
	if req.GroupID != nil && s.service.schedulerSnapshot != nil {
		schedGroup, _ = s.service.schedulerSnapshot.GetGroupByID(ctx, *req.GroupID)
	}

	filterStats := openAISelectionFilterStats{pool: len(accounts)}
	filtered := make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[account.ID]; excluded {
				filterStats.exclude("excluded")
				continue
			}
		}
		if !account.IsSchedulableForModelWithContext(ctx, req.RequestedModel) {
			filterStats.exclude("not_schedulable")
			continue
		}
		if !account.IsOpenAICompatPoolMember(poolPlatform) || !account.IsOpenAICompatible() {
			filterStats.exclude("platform_mismatch")
			continue
		}
		if s.service.isOpenAIAccountRequestRuntimeBlocked(account, req.RequestedModel) {
			filterStats.exclude("runtime_blocked")
			continue
		}
		// require_privacy_set is a group-scoped eligibility gate. Do not mutate the
		// shared account: another group may intentionally allow accounts whose
		// upstream privacy setting has not been confirmed.
		if schedGroup != nil && schedGroup.RequirePrivacySet && !account.IsPrivacySet() {
			filterStats.exclude("privacy_not_set")
			continue
		}
		if compatible, reason := s.isAccountRequestCompatibleReason(ctx, account, req); !compatible {
			filterStats.exclude(reason)
			continue
		}
		if !s.isAccountTransportCompatible(account, req.RequiredTransport) {
			filterStats.exclude("transport_incompatible")
			continue
		}
		if req.RequireCompact && openAICompactSupportTier(account) == 0 {
			filterStats.exclude("compact_unsupported")
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered, filterStats
}
