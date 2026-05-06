package service

import (
	"context"
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

// TKRecordForwardFailure records a gateway failure outcome into the pricing
// availability store. Called from the 3 handler error branches:
//   - handler/gateway_handler_chat_completions.go (non-2xx from Forward)
//   - handler/gateway_handler_responses.go        (non-2xx from Forward)
//   - gemini_v1beta_handler.go                    (non-2xx from ForwardNative)
//
// It is intentionally loose (any error string, any status code) so handler
// code stays minimal — classification logic lives in
// PricingAvailabilityService.classifyFailureKind.
//
// The method is safe to call with a nil receiver.
func (s *GatewayService) TKRecordForwardFailure(ctx context.Context, platform, modelID string, accountID int64, statusCode int, errorBody string, networkError bool) {
	if s == nil || s.tkPricingAvailability == nil {
		return
	}
	s.tkPricingAvailability.RecordOutcome(ctx, AvailabilityOutcome{
		Platform:           platform,
		ModelID:            modelID,
		AccountID:          accountID,
		Success:            false,
		UpstreamStatusCode: statusCode,
		UpstreamErrorBody:  truncateErrorBody(errorBody),
		NetworkError:       networkError,
	})
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
