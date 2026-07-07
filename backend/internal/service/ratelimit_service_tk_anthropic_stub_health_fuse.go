package service

import "net/http"

// tkAnthropicStubHealthFuseEligible gates the anthropic 3/3 temp_unschedulable
// ladder (handleAnthropicUpstreamErrorWithOptions). It is intentionally narrow:
// only infra/upstream-health signals and 429s where an authoritative account
// cooldown already landed (SetRateLimited). Permission/billing/client errors
// (400/402/403) and header-less or extra-usage 429s must NOT advance the fuse.
func tkAnthropicStubHealthFuseEligible(statusCode int, authoritativeCooldownWritten bool) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	case http.StatusTooManyRequests:
		return authoritativeCooldownWritten
	default:
		return false
	}
}
