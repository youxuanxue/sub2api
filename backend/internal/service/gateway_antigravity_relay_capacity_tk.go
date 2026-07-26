package service

import (
	"context"
	"net/http"
	"strings"
)

const (
	NoAvailableAccountsRetryAfterSeconds  = "5"
	AntigravityRelayCapacityClientMessage = "No available accounts"

	antigravityRelayCapacityReason GatewayFailureReason = "antigravity_relay_no_available_accounts"
)

func tkIsAntigravityEdgeRelayStub(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformAntigravity &&
		account.Type == AccountTypeAPIKey &&
		isEdgeMirrorStub(account, edgeIDPattern)
}

func tkIsAntigravityRelayCapacityResponse(account *Account, statusCode int, responseBody []byte) bool {
	if !tkIsAntigravityEdgeRelayStub(account) {
		return false
	}
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(responseBody))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	return tkSkipDownstreamNoAvailableAccountsPenalty(statusCode, upstreamMsg, responseBody)
}

func tkAntigravityRelayCapacityFailoverError(
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
) *UpstreamFailoverError {
	if !tkIsAntigravityRelayCapacityResponse(account, statusCode, responseBody) {
		return nil
	}
	responseHeaders := headers.Clone()
	if responseHeaders == nil {
		responseHeaders = make(http.Header)
	}
	if strings.TrimSpace(responseHeaders.Get("Retry-After")) == "" {
		responseHeaders.Set("Retry-After", NoAvailableAccountsRetryAfterSeconds)
	}
	return &UpstreamFailoverError{
		StatusCode:        statusCode,
		ResponseBody:      responseBody,
		ResponseHeaders:   responseHeaders,
		Scope:             GatewayFailureScopeAccount,
		Reason:            antigravityRelayCapacityReason,
		NextAccountAction: NextAccountRetry,
		ClientStatusCode:  http.StatusTooManyRequests,
		ClientMessage:     AntigravityRelayCapacityClientMessage,
	}
}

func newUpstreamFailoverErrorWithTKCapacity(
	account *Account,
	statusCode int,
	headers http.Header,
	responseBody []byte,
) *UpstreamFailoverError {
	if capacityErr := tkAntigravityRelayCapacityFailoverError(
		account,
		statusCode,
		headers,
		responseBody,
	); capacityErr != nil {
		return capacityErr
	}
	return &UpstreamFailoverError{
		StatusCode:      statusCode,
		ResponseBody:    responseBody,
		ResponseHeaders: headers.Clone(),
	}
}

func (s *RateLimitService) handleAntigravityRelayCapacity(
	ctx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
	requestedModel string,
) bool {
	if !tkIsAntigravityRelayCapacityResponse(account, statusCode, responseBody) {
		return false
	}
	modelKey := strings.TrimSpace(resolveFinalAntigravityModelKey(ctx, account, requestedModel))
	if modelKey == "" {
		return true
	}
	count := s.recordAntigravityRelaySaturation(ctx, account.ID, modelKey, statusCode)
	s.tkTryAntigravityRelayModelCooldown(ctx, account, count, requestedModel, modelKey)
	return true
}
