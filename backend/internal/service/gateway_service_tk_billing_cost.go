package service

import (
	"context"
)

// TK user/group rate helpers for admission, profit checks, and balance
// estimates. Formal RecordUsage settlement uses ResolveForBilling inside
// settleClaudeCustomerFacingCost (gateway_usage_billing.go), not these APIs.

// getUserGroupRateMultiplier uses Resolve: on lookup failure it returns 1 so
// non-settlement callers can keep estimating. Settlement must not charge this.
func (s *GatewayService) getUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	if s == nil {
		return groupDefaultMultiplier
	}
	resolver := s.userGroupRateResolver
	if resolver == nil {
		resolver = newUserGroupRateResolver(
			s.userGroupRateRepo,
			s.userGroupRateCache,
			resolveUserGroupRateCacheTTL(s.cfg),
			&s.userGroupRateSF,
			"service.gateway",
		)
	}
	return resolver.Resolve(ctx, userID, groupID, groupDefaultMultiplier)
}

// ResolveUserGroupRateMultiplier exposes getUserGroupRateMultiplier for
// admission/estimate callers. Settlement uses ResolveForBilling instead.
func (s *GatewayService) ResolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	return s.getUserGroupRateMultiplier(ctx, userID, groupID, groupDefaultMultiplier)
}
