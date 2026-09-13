package service

import (
	"strings"
)

// SetPricingAvailabilityService wires the optional availability-observability
// service into GatewayService without changing the upstream constructor
// signature. Called during Wire DI setup in PR-1; absent call = feature
// disabled (RecordOutcome is nil-safe).
func (s *GatewayService) SetPricingAvailabilityService(svc *PricingAvailabilityService) {
	if s != nil {
		s.tkPricingAvailability = svc
	}
}

// HasPricingAvailabilityService returns true once the availability service is
// wired. Used by wire_assertion_tk_test.go and other production-DI smoke tests
// to prove the post-construction setter actually ran (vs. silently dropped).
func (s *GatewayService) HasPricingAvailabilityService() bool {
	return s != nil && s.tkPricingAvailability != nil
}

// truncateErrorBody limits the error body that travels from handlers into the
// availability classifier. 512 bytes is enough to detect model_not_found /
// rate_limit / auth keywords without retaining PII or large upstream HTML
// error pages in-memory.
func truncateErrorBody(body string) string {
	const maxLen = 512
	body = strings.TrimSpace(body)
	if len(body) > maxLen {
		return body[:maxLen]
	}
	return body
}
