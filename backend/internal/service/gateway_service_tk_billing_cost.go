package service

import (
	"context"
)

// TK billing cost calculation: user/group rate multiplier resolution for
// GatewayService.RecordUsage. Shared billing helpers live in gateway_usage_billing.go
// (upstream); this companion keeps only TokenKey-specific multiplier logic.

// getUserGroupRateMultiplier resolves the effective rate multiplier for a
// user x group pair, with fallback to groupDefaultMultiplier.
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

// ResolveUserGroupRateMultiplier resolves the same cached multiplier used by usage billing.
func (s *GatewayService) ResolveUserGroupRateMultiplier(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	return s.getUserGroupRateMultiplier(ctx, userID, groupID, groupDefaultMultiplier)
}
