package service

import (
	"context"
	"net/http"

	newapitypes "github.com/QuantumNous/new-api/types"
)

// reportNewAPIBridgeUpstreamError feeds bridge-layer apiErr back into
// RateLimitService so newapi accounts hit by 401 / 402 / 429 / 529 / 403 enter
// the same SetError / SetRateLimited / SetOverloaded / handle403 state machine
// as direct (non-bridge) gateway paths.
//
// This is the OPC-style funnel: every Tier1 bridge call site (chat completions
// / responses / embeddings / images / anthropic-via-chat) calls this helper in
// its `if apiErr != nil` branch, so adding a sixth bridge endpoint never
// duplicates the wiring. Without this funnel, bridge errors silently bypass
// every rate-limit path — see `docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md`
// § B-1 for the field-by-field impact analysis.
//
// The body argument is best-effort: bridge errors carry an aggregated message
// rather than the upstream raw response, so detailed body parsers (e.g.
// parseOpenAIRateLimitResetTime which reads `resets_at` from the original
// upstream JSON) may fall back to the default 5-minute lock. That is still
// strictly better than the current behavior (no lock at all). When upstream
// new-api preserves the raw body in the error envelope in a future release,
// switch the body source here.
func (s *OpenAIGatewayService) reportNewAPIBridgeUpstreamError(
	ctx context.Context, account *Account, apiErr *newapitypes.NewAPIError,
) {
	if s == nil || s.rateLimitService == nil || account == nil || apiErr == nil {
		return
	}
	status := apiErr.StatusCode
	if status < 100 || status > 599 {
		status = http.StatusBadGateway
	}
	body := []byte(apiErr.Error())
	s.rateLimitService.HandleUpstreamError(ctx, account, status, nil, body)
}

// reportNewAPIBridgeUpstreamError is the GatewayService-receiver mirror used by
// the gateway_bridge_dispatch.go entry points. Two receivers because
// OpenAIGatewayService and GatewayService are independent structs; keeping
// them as methods avoids exposing internal `rateLimitService` access.
func (s *GatewayService) reportNewAPIBridgeUpstreamError(
	ctx context.Context, account *Account, apiErr *newapitypes.NewAPIError,
) {
	if s == nil || s.rateLimitService == nil || account == nil || apiErr == nil {
		return
	}
	status := apiErr.StatusCode
	if status < 100 || status > 599 {
		status = http.StatusBadGateway
	}
	body := []byte(apiErr.Error())
	s.rateLimitService.HandleUpstreamError(ctx, account, status, nil, body)
}
